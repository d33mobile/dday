// Package matrixbot implements the D-Day Matrix bot: it listens for the
// "!start" command (legacy alias: "!register") and opens a private DM with the
// sender containing a private link whose token is an age-encrypted timestamp —
// a registration link for a newcomer, a participant-panel magic link for
// someone who is already signed up.
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

// defaultRateWindow is the minimum interval between two "!start" commands
// from the same handle that the bot will act on. Further commands inside the
// window are ignored (logged), so a user spamming "!start" gets a single DM.
const defaultRateWindow = 30 * time.Second

// Client is a minimal Matrix client-server API client for the bot.
type Client struct {
	HS          string        // homeserver base URL, no trailing slash
	Token       string        // access token (set by Login)
	Self        string        // full MXID of the bot (set by Login)
	HTTP        *http.Client  // HTTP client
	Recipient   age.Recipient // age recipient used to encrypt the link token
	LinkBase    string        // registration URL the token is appended to
	PanelBase   string        // participant-panel URL the panel token is appended to
	TokenSecret string        // shared HMAC key authenticating the link token
	IsOpen      func() bool   // registration time gate; nil means always open

	// CheckRegistered reports whether a handle is already registered (and its
	// participant number), by asking the web service. nil skips the check;
	// any error is treated fail-open (proceed to issue a link, since the web
	// POST dedupes anyway).
	CheckRegistered func(handle string) (number int, registered bool, err error)

	// AllowedRooms, when non-empty, restricts which rooms Run reacts to
	// "!start" in. An empty (or nil) set reacts everywhere — the default,
	// unchanged behaviour. Populate it from the ALLOWED_ROOMS env var.
	AllowedRooms map[string]bool

	// RateWindow is the minimum interval between two "!start" from the same
	// handle that the bot acts on; commands inside the window are ignored. Zero
	// uses defaultRateWindow.
	RateWindow time.Duration

	// CachePath, when non-empty, is the path to a JSON file that persists the
	// dmRooms handle->roomID map across restarts. LoadCache reads it at startup
	// and cacheDM rewrites it (atomically) on every change. Empty disables all
	// disk I/O — behaviour is exactly as if the cache were purely in-memory.
	CachePath string

	// AnnounceRoom is the room new registrations are announced in (typically the
	// home channel, MATRIX_ROOM). Empty disables announcements entirely.
	AnnounceRoom string

	// AnnounceInterval is how often the announce loop polls the web service for
	// new registrations. Zero or negative uses defaultAnnounceInterval.
	AnnounceInterval time.Duration

	// FetchNewRegistrations returns the registrations with id > sinceID, in id
	// order, by querying the web service's internal feed. nil disables
	// announcements (as does an empty AnnounceRoom).
	FetchNewRegistrations func(sinceID int) ([]NewRegistration, error)

	// now returns the current time; overridable in tests to drive the rate
	// limiter deterministically. nil means time.Now.
	now func() time.Time

	// mu guards the anti-spam maps below.
	mu       sync.Mutex
	dmRooms  map[string]string    // handle -> cached DM room id (reused across commands)
	lastSeen map[string]time.Time // handle -> last acted-on "!start" time

	// lastAnnounced is the highest registration id already announced. It is
	// persisted with the DM cache so a restart does not re-announce.
	lastAnnounced int
	// baselined records that the announce loop has established its starting
	// point, so the "first run without state" branch runs at most once.
	baselined bool
}

// NewRegistration is one signup as reported by the web service's internal
// registrations feed. It intentionally carries no e-mail or city: announcements
// go to a public room, so personal data must not even be transported here.
// The json tags match the web service's /api/registrations feed.
type NewRegistration struct {
	ID          int    `json:"id"`
	Handle      string `json:"handle"`
	Nick        string `json:"nick"`
	Rank        int    `json:"rank"`
	Confirmed   bool   `json:"confirmed"`
	WaitlistPos int    `json:"waitlistPos"`
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

// roomAllowed reports whether Run should react to "!start" in roomID. With
// no allowlist configured every room is allowed.
func (c *Client) roomAllowed(roomID string) bool {
	if len(c.AllowedRooms) == 0 {
		return true
	}
	return c.AllowedRooms[roomID]
}

// rateLimited reports whether a "!start" from handle arrives too soon after
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

// Run drives the sync loop, reacting to "!start" until ctx is cancelled.
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
	log.Printf("listening for !start (bot is %s)", c.Self)

	// Announcements run on their own clock, independent of the sync loop: the
	// registration completes on the web side, so the only way the bot learns
	// about it is by polling. The goroutine exits with ctx.
	if c.announcementsEnabled() {
		go c.announceLoop(ctx)
	} else {
		log.Printf("registration announcements disabled (no announce room or no feed)")
	}

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
				if ev.Sender == c.Self || !isStartCmd(ev.Content.Body) {
					continue
				}
				log.Printf("!start from %s in %s", ev.Sender, roomID)
				if _, err := c.HandleRegister(roomID, ev.Sender); err != nil {
					log.Printf("!start for %s failed: %v", ev.Sender, err)
				}
			}
		}
	}
}

// defaultAnnounceInterval is how often the announce loop polls the web service
// for registrations it has not announced yet.
const defaultAnnounceInterval = 30 * time.Second

// announcementsEnabled reports whether the bot should announce new signups: it
// needs both a target room and a way to fetch them.
func (c *Client) announcementsEnabled() bool {
	return c.AnnounceRoom != "" && c.FetchNewRegistrations != nil
}

// announceLoop polls for new registrations every AnnounceInterval and posts
// them in AnnounceRoom, until ctx is done. It owns no logic beyond the ticking:
// one poll-and-post cycle is announceOnce, which tests drive directly.
func (c *Client) announceLoop(ctx context.Context) {
	every := c.AnnounceInterval
	if every <= 0 {
		every = defaultAnnounceInterval
	}
	log.Printf("announcing new registrations in %s every %s", c.AnnounceRoom, every)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := c.announceOnce(); err != nil {
			// A failing poll or send must not kill the loop: the next tick
			// retries from the same lastAnnounced, so nothing is skipped.
			log.Printf("announce cycle: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// announceOnce runs a single announce cycle: fetch everything newer than
// lastAnnounced and post one message per registration in AnnounceRoom, then
// persist the new high-water mark.
//
// The very first cycle after a start without persisted state does not post
// anything — it only adopts the current maximum id as the baseline, so a bot
// deployed against an already-populated database does not spam the room with
// the whole history. Only signups arriving after that are announced.
func (c *Client) announceOnce() error {
	if !c.announcementsEnabled() {
		return nil
	}
	c.mu.Lock()
	since := c.lastAnnounced
	baseline := !c.baselined && since == 0
	c.mu.Unlock()

	regs, err := c.FetchNewRegistrations(since)
	if err != nil {
		return fmt.Errorf("fetch registrations since %d: %w", since, err)
	}

	if baseline {
		max := since
		for _, r := range regs {
			if r.ID > max {
				max = r.ID
			}
		}
		c.mu.Lock()
		c.baselined = true
		c.lastAnnounced = max
		c.persistLocked()
		c.mu.Unlock()
		log.Printf("announce baseline set to registration #%d (%d existing signup(s) not announced)", max, len(regs))
		return nil
	}

	var firstErr error
	for _, r := range regs {
		if r.ID <= since {
			continue
		}
		plain, html := announceText(r)
		if err := c.sendHTML(c.AnnounceRoom, plain, html); err != nil {
			// Stop at the first failure and keep lastAnnounced where it is, so
			// the next cycle retries this registration instead of losing it.
			if firstErr == nil {
				firstErr = fmt.Errorf("announce registration #%d: %w", r.ID, err)
			}
			break
		}
		c.mu.Lock()
		if r.ID > c.lastAnnounced {
			c.lastAnnounced = r.ID
		}
		c.baselined = true
		c.persistLocked()
		c.mu.Unlock()
	}
	return firstErr
}

// announceText renders the public announcement for one registration, as a
// (plain, html) pair. It uses only the Matrix handle and the participant
// number/waiting-list position — never the e-mail or city, which stay on the
// web side and must not reach a public room.
func announceText(r NewRegistration) (string, string) {
	name := localpart(r.Handle)
	link := mention(r.Handle)
	if r.Confirmed {
		return fmt.Sprintf("🎉 %s dołącza do D-Day — uczestnik #%d", name, r.ID),
			fmt.Sprintf("🎉 %s dołącza do D-Day — uczestnik #%d", link, r.ID)
	}
	return fmt.Sprintf("🎉 %s zapisał(a) się na D-Day — lista rezerwowa, pozycja #%d", name, r.WaitlistPos),
		fmt.Sprintf("🎉 %s zapisał(a) się na D-Day — lista rezerwowa, pozycja #%d", link, r.WaitlistPos)
}

// isStartCmd reports whether body's first word is the bot's command. "!start"
// is the official command; "!register" stays accepted as a legacy alias,
// because it was documented on the landing page before the rename and people
// still have it in their muscle memory. Matching is case-insensitive and
// ignores anything after the first word.
func isStartCmd(body string) bool {
	f := strings.Fields(strings.TrimSpace(body))
	return len(f) > 0 && (strings.EqualFold(f[0], "!start") || strings.EqualFold(f[0], "!register"))
}

// HandleRegister reacts to a "!start" that arrived in originRoom from user.
//
// A private DM is opened for one reason only: to deliver a private link. The
// branches are:
//
//   - already registered: a DM with the participant-panel magic link (where the
//     user can check their status and withdraw), plus a public nudge in
//     originRoom. If no panel link can be built (PanelBase unset or an encoding
//     error) it degrades to a public "you are already signed up" reply, so the
//     command never fails silently;
//   - not open yet (per c.IsOpen): a public "sign-ups haven't started" reply
//     carrying the start time — no DM is opened;
//   - open: a DM with the registration link, plus a public nudge back in
//     originRoom pointing the user at their DMs.
//
// For the public branches an empty originRoom means the reply is only logged.
// It returns the DM room id for the DM branches, and "" for the public ones.
func (c *Client) HandleRegister(originRoom, user string) (string, error) {
	// Per-handle rate limit: drop a burst of "!start" from the same user so a
	// single command yields a single DM. Returns an empty room id, no error.
	if c.rateLimited(user) {
		log.Printf("!start from %s ignored (rate limited)", user)
		return "", nil
	}

	// Already-registered wins over everything. Fail-open: on error we log and
	// fall through to the normal flow, since the web POST dedupes anyway.
	if c.CheckRegistered != nil {
		number, registered, err := c.CheckRegistered(user)
		if err != nil {
			log.Printf("check registered for %s: %v (proceeding)", user, err)
		} else if registered {
			return c.sendPanelLink(originRoom, user, number)
		}
	}

	// Registration not open yet: reply publicly in originRoom with the start
	// time — no DM is opened.
	if c.IsOpen != nil && !c.IsOpen() {
		name := localpart(user)
		plain := fmt.Sprintf("Hej %s, zapisy jeszcze nie wystartowały — ruszają w %s. Wróć wtedy i napisz !start.", name, regwindow.OpenStartText())
		html := fmt.Sprintf("Hej %s, zapisy jeszcze nie wystartowały — ruszają w <b>%s</b>. Wróć wtedy i napisz <code>!start</code>.", mention(user), regwindow.OpenStartText())
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

// sendPanelLink handles the already-registered branch: it DMs user a magic link
// to their participant panel (status + withdrawing) and nudges them publicly in
// originRoom. The link is private, so delivering it by DM keeps the "a DM is
// only ever opened to deliver a private link" rule; the participant number is
// mentioned in the DM only, never in the channel.
//
// Every failure degrades to the old public "you are already signed up" reply
// rather than an error: an unconfigured PanelBase or a token/DM problem must
// not leave the user without an answer.
func (c *Client) sendPanelLink(originRoom, user string, number int) (string, error) {
	link, err := c.panelLink(user)
	if err != nil {
		log.Printf("panel link for %s: %v (falling back to public reply)", user, err)
		return c.replyAlreadyRegistered(originRoom, user)
	}
	dmRoom, _, err := c.resolveDM(originRoom, user)
	if err != nil {
		log.Printf("resolve dm for %s: %v (falling back to public reply)", user, err)
		return c.replyAlreadyRegistered(originRoom, user)
	}

	who := "Jesteś już zapisany 🎉"
	if number > 0 {
		who = fmt.Sprintf("Jesteś już zapisany 🎉 (#%d)", number)
	}
	plain := fmt.Sprintf("%s Oto link do Twojego panelu — możesz tam sprawdzić status i w razie czego wycofać udział: %s", who, link)
	html := fmt.Sprintf("%s Oto link do Twojego panelu — możesz tam sprawdzić status i w razie czego wycofać udział:<br><a href=%q>otwórz panel</a>", who, link)
	if err := c.sendHTML(dmRoom, plain, html); err != nil {
		log.Printf("send panel link to %s: %v (falling back to public reply)", user, err)
		return c.replyAlreadyRegistered(originRoom, user)
	}
	c.nudge(originRoom, dmRoom, user, "wysłałem Ci tam link do Twojego panelu.")
	return dmRoom, nil
}

// replyAlreadyRegistered is the link-less fallback for the already-registered
// branch: a public reply in originRoom that deliberately omits the participant
// number so it is not leaked in a channel.
func (c *Client) replyAlreadyRegistered(originRoom, user string) (string, error) {
	plain := fmt.Sprintf("Hej %s, jesteś już zapisany 🎉 Ponowna rejestracja nie jest możliwa.", localpart(user))
	html := fmt.Sprintf("Hej %s, jesteś już zapisany 🎉 Ponowna rejestracja nie jest możliwa.", mention(user))
	return c.replyPublic(originRoom, plain, html)
}

// replyPublic sends a public reply into originRoom (the channel the "!start"
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

// nudge posts a short public reply in originRoom (the channel the "!start"
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

// resolveDM decides which room the private reply for user should go to,
// given the room the "!start" arrived in.
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
	c.persistLocked()
}

// cacheFile is the on-disk shape of the bot state at CachePath: the DM room
// cache plus the id of the last registration announced. It replaced the bare
// handle->roomID map; loadCacheFile still accepts that legacy flat form.
type cacheFile struct {
	DMs           map[string]string `json:"dms"`
	LastAnnounced int               `json:"lastAnnounced"`
}

// persistLocked writes the current state to CachePath. It is best-effort (an
// I/O error is logged, never propagated) and a no-op without a CachePath.
// Callers must hold c.mu — the file always reflects a consistent snapshot.
func (c *Client) persistLocked() {
	if c.CachePath == "" {
		return
	}
	if err := writeCacheFile(c.CachePath, cacheFile{DMs: c.dmRooms, LastAnnounced: c.lastAnnounced}); err != nil {
		log.Printf("persist bot cache to %s: %v", c.CachePath, err)
	}
}

// writeCacheFile atomically writes the state as JSON to path. It writes to a
// temporary file in the same directory and renames it into place, so a crash
// mid-write never leaves a truncated/corrupt cache. Callers hold the lock
// guarding the state, so the snapshot is consistent.
func writeCacheFile(path string, state cacheFile) error {
	if state.DMs == nil {
		state.DMs = map[string]string{}
	}
	data, err := json.Marshal(state)
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

// LoadCache populates the in-memory state (DM rooms and lastAnnounced) from the
// JSON file at CachePath. A missing file is not an error (the bot simply starts
// empty). A present-but-corrupt file is logged and treated as empty rather than
// fatal, so a mangled cache cannot crash-loop the bot. It returns the number of
// DM entries loaded. When CachePath is empty it is a no-op returning 0.
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
	loaded, ok := parseCacheFile(data)
	if !ok {
		log.Printf("bot cache %s is corrupt; starting empty", c.CachePath)
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dmRooms == nil {
		c.dmRooms = make(map[string]string)
	}
	for user, room := range loaded.DMs {
		if user != "" && room != "" {
			c.dmRooms[user] = room
		}
	}
	c.lastAnnounced = loaded.LastAnnounced
	return len(c.dmRooms), nil
}

// parseCacheFile decodes the cache, accepting both the current object form
// ({"dms":{...},"lastAnnounced":N}) and the legacy flat handle->roomID map
// written by older builds. The legacy form loads with lastAnnounced 0, which
// makes the bot re-baseline on the current max id instead of replaying history.
func parseCacheFile(data []byte) (cacheFile, bool) {
	var cur cacheFile
	if err := json.Unmarshal(data, &cur); err == nil && cur.DMs != nil {
		return cur, true
	}
	var legacy map[string]string
	if err := json.Unmarshal(data, &legacy); err == nil {
		return cacheFile{DMs: legacy}, true
	}
	return cacheFile{}, false
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
