// Package matrixbot implements the D-Day Matrix bot: it listens for the
// "!register" command and opens a private DM with the sender containing a
// registration link whose token is an age-encrypted timestamp.
//
// The logic lives in this importable package (not in package main) so the
// end-to-end test can drive the real code paths.
package matrixbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"filippo.io/age"

	"github.com/d33mobile/dday/internal/regwindow"
)

// defaultRateWindow is the minimum interval between two "!register" commands
// from the same handle that the bot will act on. Further commands inside the
// window are ignored (logged), so a user spamming "!register" gets a single DM.
const defaultRateWindow = 30 * time.Second

// Client is a minimal Matrix client-server API client for the bot.
type Client struct {
	HS          string        // homeserver base URL, no trailing slash
	Token       string        // access token (set by Login)
	Self        string        // full MXID of the bot (set by Login)
	HTTP        *http.Client  // HTTP client
	Recipient   age.Recipient // age recipient used to encrypt the link token
	LinkBase    string        // registration URL the token is appended to
	TokenSecret string        // shared HMAC key authenticating the link token
	IsOpen      func() bool   // registration time gate; nil means always open

	// CheckRegistered reports whether a handle is already registered (and its
	// participant number), by asking the web service. nil skips the check;
	// any error is treated fail-open (proceed to issue a link, since the web
	// POST dedupes anyway).
	CheckRegistered func(handle string) (number int, registered bool, err error)

	// AllowedRooms, when non-empty, restricts which rooms Run reacts to
	// "!register" in. An empty (or nil) set reacts everywhere — the default,
	// unchanged behaviour. Populate it from the ALLOWED_ROOMS env var.
	AllowedRooms map[string]bool

	// RateWindow is the minimum interval between two "!register" from the same
	// handle that the bot acts on; commands inside the window are ignored. Zero
	// uses defaultRateWindow.
	RateWindow time.Duration

	// CachePath, when non-empty, is the path to a JSON file that persists the
	// dmRooms handle->roomID map across restarts. LoadCache reads it at startup
	// and cacheDM rewrites it (atomically) on every change. Empty disables all
	// disk I/O — behaviour is exactly as if the cache were purely in-memory.
	CachePath string

	// now returns the current time; overridable in tests to drive the rate
	// limiter deterministically. nil means time.Now.
	now func() time.Time

	// mu guards the anti-spam maps below.
	mu       sync.Mutex
	dmRooms  map[string]string    // handle -> cached DM room id (reused across commands)
	lastSeen map[string]time.Time // handle -> last acted-on "!register" time
}

// New returns a Client for the given homeserver.
func New(hs string) *Client {
	return &Client{
		HS:       strings.TrimRight(hs, "/"),
		HTTP:     &http.Client{Timeout: 60 * time.Second},
		dmRooms:  make(map[string]string),
		lastSeen: make(map[string]time.Time),
	}
}

// ParseAllowedRooms parses a comma-separated room-id list (the ALLOWED_ROOMS
// env var) into a set. Blank entries are skipped; an empty result means "react
// everywhere".
func ParseAllowedRooms(s string) map[string]bool {
	m := make(map[string]bool)
	for _, part := range strings.Split(s, ",") {
		if r := strings.TrimSpace(part); r != "" {
			m[r] = true
		}
	}
	return m
}

// clock returns the current time, honouring the injectable c.now for tests.
func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// roomAllowed reports whether Run should react to "!register" in roomID. With
// no allowlist configured every room is allowed.
func (c *Client) roomAllowed(roomID string) bool {
	if len(c.AllowedRooms) == 0 {
		return true
	}
	return c.AllowedRooms[roomID]
}

// rateLimited reports whether a "!register" from handle arrives too soon after
// the last one the bot acted on. On the accepted path it records the time so
// the next command starts a fresh window. Safe for concurrent use.
func (c *Client) rateLimited(handle string) bool {
	window := c.RateWindow
	if window <= 0 {
		window = defaultRateWindow
	}
	now := c.clock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastSeen == nil {
		c.lastSeen = make(map[string]time.Time)
	}
	if last, ok := c.lastSeen[handle]; ok && now.Sub(last) < window {
		return true
	}
	c.lastSeen[handle] = now
	return false
}

// Login authenticates with a password and stores the access token.
func (c *Client) Login(user, pass string) error {
	body := map[string]any{
		"type":                        "m.login.password",
		"identifier":                  map[string]any{"type": "m.id.user", "user": user},
		"password":                    pass,
		"initial_device_display_name": "ddaybot",
	}
	var out struct {
		AccessToken string `json:"access_token"`
		UserID      string `json:"user_id"`
	}
	if err := c.do("POST", "/_matrix/client/v3/login", body, &out); err != nil {
		return err
	}
	if out.AccessToken == "" {
		return fmt.Errorf("login: no access token returned")
	}
	c.Token = out.AccessToken
	if out.UserID != "" {
		c.Self = out.UserID
	}
	return nil
}

type syncResp struct {
	NextBatch string `json:"next_batch"`
	Rooms     struct {
		Join   map[string]joinedRoom `json:"join"`
		Invite map[string]any        `json:"invite"`
	} `json:"rooms"`
}

type joinedRoom struct {
	Timeline struct {
		Events []event `json:"events"`
	} `json:"timeline"`
}

type event struct {
	Type    string `json:"type"`
	Sender  string `json:"sender"`
	EventID string `json:"event_id"`
	Content struct {
		MsgType string `json:"msgtype"`
		Body    string `json:"body"`
	} `json:"content"`
}

func (c *Client) sync(since string, timeoutMS int) (*syncResp, error) {
	q := url.Values{}
	q.Set("timeout", fmt.Sprintf("%d", timeoutMS))
	if since != "" {
		q.Set("since", since)
	}
	var out syncResp
	if err := c.do("GET", "/_matrix/client/v3/sync?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Run drives the sync loop, reacting to "!register" until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	// Prime with an initial sync so we only react to messages from now on,
	// not to historical backlog.
	first, err := c.sync("", 0)
	if err != nil {
		return fmt.Errorf("initial sync: %w", err)
	}
	since := first.NextBatch
	for roomID := range first.Rooms.Invite {
		c.joinRoom(roomID)
	}
	log.Printf("listening for !register (bot is %s)", c.Self)

	for {
		if ctx.Err() != nil {
			return nil
		}
		res, err := c.sync(since, 30000)
		if err != nil {
			log.Printf("sync error: %v (retrying in 5s)", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
			continue
		}
		since = res.NextBatch

		for roomID := range res.Rooms.Invite {
			log.Printf("invited to %s, joining", roomID)
			c.joinRoom(roomID)
		}
		for roomID, room := range res.Rooms.Join {
			if !c.roomAllowed(roomID) {
				continue
			}
			for _, ev := range room.Timeline.Events {
				if ev.Type != "m.room.message" || ev.Content.MsgType != "m.text" {
					continue
				}
				if ev.Sender == c.Self || !isRegisterCmd(ev.Content.Body) {
					continue
				}
				log.Printf("!register from %s in %s", ev.Sender, roomID)
				if _, err := c.HandleRegister(roomID, ev.Sender); err != nil {
					log.Printf("register for %s failed: %v", ev.Sender, err)
				}
			}
		}
	}
}

func isRegisterCmd(body string) bool {
	f := strings.Fields(strings.TrimSpace(body))
	return len(f) > 0 && strings.EqualFold(f[0], "!register")
}

// HandleRegister reacts to a "!register" that arrived in originRoom from user.
//
// A private DM is opened for one reason only: to deliver a private registration
// link. Every other outcome is answered publicly in originRoom, @-mentioning
// the user, without spawning a DM:
//
//   - already registered: a public "you are already signed up, re-registration
//     is not possible" reply (the participant number is not revealed in public);
//   - not open yet (per c.IsOpen): a public "sign-ups haven't started" reply
//     carrying the start time;
//   - open: a DM with the registration link, plus a public nudge back in
//     originRoom pointing the user at their DMs.
//
// For the public branches an empty originRoom means the reply is only logged.
// It returns the DM room id for the open branch, and "" for the public ones.
func (c *Client) HandleRegister(originRoom, user string) (string, error) {
	// Per-handle rate limit: drop a burst of "!register" from the same user so a
	// single command yields a single DM. Returns an empty room id, no error.
	if c.rateLimited(user) {
		log.Printf("!register from %s ignored (rate limited)", user)
		return "", nil
	}

	// Already-registered wins over everything. Fail-open: on error we log and
	// fall through to the normal flow, since the web POST dedupes anyway. The
	// reply is public in originRoom — no DM is opened, and the participant
	// number is deliberately omitted so it is not leaked in a channel.
	if c.CheckRegistered != nil {
		_, registered, err := c.CheckRegistered(user)
		if err != nil {
			log.Printf("check registered for %s: %v (proceeding)", user, err)
		} else if registered {
			name := localpart(user)
			plain := fmt.Sprintf("Hej %s, jesteś już zapisany 🎉 Ponowna rejestracja nie jest możliwa.", name)
			html := fmt.Sprintf("Hej %s, jesteś już zapisany 🎉 Ponowna rejestracja nie jest możliwa.", mention(user))
			return c.replyPublic(originRoom, plain, html)
		}
	}

	// Registration not open yet: reply publicly in originRoom with the start
	// time — no DM is opened.
	if c.IsOpen != nil && !c.IsOpen() {
		name := localpart(user)
		plain := fmt.Sprintf("Hej %s, zapisy jeszcze nie wystartowały — ruszają w %s. Wróć wtedy i napisz !register.", name, regwindow.OpenStartText())
		html := fmt.Sprintf("Hej %s, zapisy jeszcze nie wystartowały — ruszają w <b>%s</b>. Wróć wtedy i napisz <code>!register</code>.", mention(user), regwindow.OpenStartText())
		return c.replyPublic(originRoom, plain, html)
	}

	link, err := c.registerLink(user)
	if err != nil {
		return "", fmt.Errorf("build link: %w", err)
	}
	dmRoom, _, err := c.resolveDM(originRoom, user)
	if err != nil {
		return "", fmt.Errorf("resolve dm: %w", err)
	}
	plain := fmt.Sprintf("Cześć! Zapisy na D-Day (unconference w Hakierspejsie): %s", link)
	html := fmt.Sprintf("Cześć! Zapisy na <b>D-Day</b> (unconference w Hakierspejsie):<br><a href=%q>zarejestruj się</a>", link)
	if err := c.sendHTML(dmRoom, plain, html); err != nil {
		return dmRoom, fmt.Errorf("send: %w", err)
	}
	c.nudge(originRoom, dmRoom, user, "wysłałem Ci tam link do rejestracji.")
	return dmRoom, nil
}

// replyPublic sends a public reply into originRoom (the channel the "!register"
// came from). When originRoom is unknown ("") it only logs — nothing is sent
// and no DM is ever opened — so a command from an unknown context is a no-op
// rather than spawning a private chat. It always returns ("", nil): the caller
// exposes "no DM room was created".
func (c *Client) replyPublic(originRoom, plain, html string) (string, error) {
	if originRoom == "" {
		log.Printf("replyPublic: no origin room, dropping message: %s", plain)
		return "", nil
	}
	if err := c.sendHTML(originRoom, plain, html); err != nil {
		return "", fmt.Errorf("send: %w", err)
	}
	return "", nil
}

// mention renders an @-mention of user as a matrix.to HTML anchor whose text is
// the localpart, matching the ping pattern used by nudge so the user is
// notified.
func mention(user string) string {
	return fmt.Sprintf("<a href=%q>%s</a>", "https://matrix.to/#/"+user, localpart(user))
}

// nudge posts a short public reply in originRoom (the channel the "!register"
// came from), @-mentioning the user and pointing them at their DMs. It is a
// best-effort side channel: it is skipped when originRoom is unknown or is the
// DM itself, and any send error is only logged.
func (c *Client) nudge(originRoom, dmRoom, user, tail string) {
	if originRoom == "" || originRoom == dmRoom {
		return
	}
	name := localpart(user)
	plain := fmt.Sprintf("Hej %s, sprawdź prywatne wiadomości - %s", name, tail)
	html := fmt.Sprintf("Hej <a href=%q>%s</a>, sprawdź prywatne wiadomości - %s",
		"https://matrix.to/#/"+user, name, tail)
	if err := c.sendHTML(originRoom, plain, html); err != nil {
		log.Printf("nudge in %s: %v", originRoom, err)
	}
}

// localpart turns a Matrix MXID "@alice:hs.org" into "alice"; any other shape
// is returned unchanged.
func localpart(mxid string) string {
	s := strings.TrimPrefix(mxid, "@")
	if i := strings.IndexByte(s, ':'); i > 0 {
		return s[:i]
	}
	return s
}

// resolveDM decides which room the registration reply for user should go to,
// given the room the "!register" arrived in.
//
//   - If originRoom is itself a 1:1 DM with user (exactly the bot and user are
//     joined), that room is reused and isOrigin is true — the user wrote from
//     their own private chat, so no public nudge is warranted.
//   - Otherwise an existing DM is reused (in-memory cache, then the m.direct
//     account data, so a restart doesn't spawn a duplicate) or a fresh one is
//     created; isOrigin is false.
func (c *Client) resolveDM(originRoom, user string) (roomID string, isOrigin bool, err error) {
	if originRoom != "" && c.isDMWith(originRoom, user) {
		return originRoom, true, nil
	}
	room, err := c.existingOrNewDM(user)
	if err != nil {
		return "", false, err
	}
	return room, false, nil
}

// isDMWith reports whether roomID is a 1:1 private chat between the bot and
// user: exactly two joined members, the bot and user. Any query error is
// treated as "not a DM" (fail-safe: fall through to normal DM handling).
func (c *Client) isDMWith(roomID, user string) bool {
	var out struct {
		Joined map[string]json.RawMessage `json:"joined"`
	}
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/joined_members", url.PathEscape(roomID))
	if err := c.do("GET", path, nil, &out); err != nil {
		log.Printf("joined_members %s: %v (treating as non-DM)", roomID, err)
		return false
	}
	if len(out.Joined) != 2 {
		return false
	}
	_, hasBot := out.Joined[c.Self]
	_, hasUser := out.Joined[user]
	return hasBot && hasUser
}

// existingOrNewDM returns a DM room with user, preferring an existing one over
// handing the user a fresh, empty chat. Lookup order: the in-memory cache, then
// the m.direct account data (which survives a bot restart), and only failing
// both does it POST /createRoom. A freshly created room is recorded in the
// cache and appended to m.direct. m.direct maintenance is best-effort — its
// errors are logged, never fatal.
func (c *Client) existingOrNewDM(user string) (string, error) {
	c.mu.Lock()
	if room := c.dmRooms[user]; room != "" {
		c.mu.Unlock()
		return room, nil
	}
	c.mu.Unlock()

	if room := c.directRoomFor(user); room != "" {
		c.cacheDM(user, room)
		return room, nil
	}

	body := map[string]any{
		"preset":    "trusted_private_chat",
		"is_direct": true,
		"invite":    []string{user},
	}
	var out struct {
		RoomID string `json:"room_id"`
	}
	if err := c.do("POST", "/_matrix/client/v3/createRoom", body, &out); err != nil {
		return "", err
	}
	c.cacheDM(user, out.RoomID)
	c.recordDirect(user, out.RoomID)
	return out.RoomID, nil
}

// cacheDM records room as the DM for user in the in-memory cache and, when a
// CachePath is configured, persists the whole cache to disk. The persist is
// best-effort: an I/O error is logged but never propagated, so a failing disk
// cannot break DM handling. The write happens under c.mu so the on-disk file
// always reflects a consistent snapshot and concurrent writers cannot lose an
// update; the cache is tiny, so holding the lock across the write is cheap.
func (c *Client) cacheDM(user, room string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dmRooms == nil {
		c.dmRooms = make(map[string]string)
	}
	c.dmRooms[user] = room
	if c.CachePath == "" {
		return
	}
	if err := writeCacheFile(c.CachePath, c.dmRooms); err != nil {
		log.Printf("persist DM cache to %s: %v", c.CachePath, err)
	}
}

// writeCacheFile atomically writes the handle->roomID map as JSON to path. It
// writes to a temporary file in the same directory and renames it into place,
// so a crash mid-write never leaves a truncated/corrupt cache. Callers hold the
// lock guarding the map, so the snapshot is consistent.
func writeCacheFile(path string, rooms map[string]string) error {
	data, err := json.Marshal(rooms)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".dm_cache-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// LoadCache populates the in-memory dmRooms map from the JSON file at CachePath.
// A missing file is not an error (the bot simply starts with an empty cache). A
// present-but-corrupt file is logged and treated as empty rather than fatal, so
// a mangled cache cannot crash-loop the bot. It returns the number of entries
// loaded. When CachePath is empty it is a no-op returning 0.
func (c *Client) LoadCache() (int, error) {
	if c.CachePath == "" {
		return 0, nil
	}
	data, err := os.ReadFile(c.CachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var loaded map[string]string
	if err := json.Unmarshal(data, &loaded); err != nil {
		log.Printf("DM cache %s is corrupt (%v); starting empty", c.CachePath, err)
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dmRooms == nil {
		c.dmRooms = make(map[string]string)
	}
	for user, room := range loaded {
		if user != "" && room != "" {
			c.dmRooms[user] = room
		}
	}
	return len(c.dmRooms), nil
}

// directMap is the shape of the m.direct account data: a mapping from a user's
// MXID to the list of DM room ids the bot shares with them.
type directMap map[string][]string

// fetchDirect reads the bot's m.direct account data. A 404 (never set) or any
// other error yields a nil map; callers treat that as "no known DMs".
func (c *Client) fetchDirect() directMap {
	if c.Self == "" {
		return nil
	}
	path := fmt.Sprintf("/_matrix/client/v3/user/%s/account_data/m.direct", url.PathEscape(c.Self))
	var out directMap
	if err := c.do("GET", path, nil, &out); err != nil {
		return nil
	}
	return out
}

// directRoomFor returns the first DM room the m.direct account data records for
// user, or "" if none is known.
func (c *Client) directRoomFor(user string) string {
	for _, room := range c.fetchDirect()[user] {
		if room != "" {
			return room
		}
	}
	return ""
}

// recordDirect appends room to the m.direct account-data entry for user, so the
// DM stays rediscoverable across a bot restart. Best-effort: any error (and the
// case where the bot's MXID is unknown) is logged and swallowed.
func (c *Client) recordDirect(user, room string) {
	if c.Self == "" || room == "" {
		return
	}
	direct := c.fetchDirect()
	if direct == nil {
		direct = directMap{}
	}
	for _, r := range direct[user] {
		if r == room {
			return // already present, nothing to write
		}
	}
	direct[user] = append(direct[user], room)
	path := fmt.Sprintf("/_matrix/client/v3/user/%s/account_data/m.direct", url.PathEscape(c.Self))
	if err := c.do("PUT", path, direct, nil); err != nil {
		log.Printf("update m.direct for %s: %v", user, err)
	}
}

func (c *Client) joinRoom(roomID string) {
	if err := c.do("POST", "/_matrix/client/v3/join/"+url.PathEscape(roomID), map[string]any{}, nil); err != nil {
		log.Printf("join %s: %v", roomID, err)
	}
}

var txnCounter atomic.Int64

func (c *Client) sendHTML(roomID, plain, html string) error {
	txn := fmt.Sprintf("ddaybot-%d-%d", time.Now().UnixNano(), txnCounter.Add(1))
	body := map[string]any{
		"msgtype":        "m.text",
		"body":           plain,
		"format":         "org.matrix.custom.html",
		"formatted_body": html,
	}
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/%s",
		url.PathEscape(roomID), url.PathEscape(txn))
	return c.do("PUT", path, body, nil)
}

func (c *Client) do(method, path string, in, out any) error {
	var reader io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.HS+path, reader)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s -> %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return nil
}
