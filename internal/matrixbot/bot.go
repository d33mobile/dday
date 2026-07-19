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
	"strings"
	"time"

	"filippo.io/age"
)

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
}

// New returns a Client for the given homeserver.
func New(hs string) *Client {
	return &Client{
		HS:   strings.TrimRight(hs, "/"),
		HTTP: &http.Client{Timeout: 60 * time.Second},
	}
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
// It always DMs the user privately, and — unless originRoom is empty or is the
// DM itself — also posts a short public nudge back in originRoom pointing the
// user at their DMs.
//
// The DM content depends on state: an already-registered user is told
// re-registration is not possible (no link); when registration is closed (per
// c.IsOpen) they get a "not open yet" notice (no link); otherwise they get a
// fresh registration link. It returns the created DM room id.
func (c *Client) HandleRegister(originRoom, user string) (string, error) {
	// Already-registered wins over everything. Fail-open: on error we log and
	// fall through to the normal flow, since the web POST dedupes anyway.
	if c.CheckRegistered != nil {
		number, registered, err := c.CheckRegistered(user)
		if err != nil {
			log.Printf("check registered for %s: %v (proceeding)", user, err)
		} else if registered {
			dmRoom, err := c.createDM(user)
			if err != nil {
				return "", fmt.Errorf("create dm: %w", err)
			}
			plain := fmt.Sprintf("Jesteś już zapisany (#%d). Ponowna rejestracja nie jest możliwa.", number)
			html := fmt.Sprintf("Jesteś już zapisany (<b>#%d</b>). Ponowna rejestracja nie jest możliwa.", number)
			if err := c.sendHTML(dmRoom, plain, html); err != nil {
				return dmRoom, fmt.Errorf("send: %w", err)
			}
			c.nudge(originRoom, dmRoom, user, "napisałem Ci szczegóły na priv.")
			return dmRoom, nil
		}
	}

	if c.IsOpen != nil && !c.IsOpen() {
		dmRoom, err := c.createDM(user)
		if err != nil {
			return "", fmt.Errorf("create dm: %w", err)
		}
		plain := "Zapisy na D-Day nie są jeszcze otwarte. Start: niedziela 26 lipca 2026, 15:00 (czasu polskiego). Napisz !register ponownie po tym terminie."
		html := "Zapisy na <b>D-Day</b> nie są jeszcze otwarte.<br>Start: niedziela 26 lipca 2026, 15:00 (czasu polskiego).<br>Napisz <code>!register</code> ponownie po tym terminie."
		if err := c.sendHTML(dmRoom, plain, html); err != nil {
			return dmRoom, fmt.Errorf("send: %w", err)
		}
		c.nudge(originRoom, dmRoom, user, "napisałem Ci szczegóły na priv.")
		return dmRoom, nil
	}

	link, err := c.registerLink(user)
	if err != nil {
		return "", fmt.Errorf("build link: %w", err)
	}
	dmRoom, err := c.createDM(user)
	if err != nil {
		return "", fmt.Errorf("create dm: %w", err)
	}
	plain := fmt.Sprintf("Cześć! Zapisy na D-Day (unconference w Hakierspejsie): %s", link)
	html := fmt.Sprintf("Cześć! Zapisy na <b>D-Day</b> (unconference w Hakierspejsie):<br><a href=%q>zarejestruj się</a>", link)
	if err := c.sendHTML(dmRoom, plain, html); err != nil {
		return dmRoom, fmt.Errorf("send: %w", err)
	}
	c.nudge(originRoom, dmRoom, user, "wysłałem Ci tam link do rejestracji.")
	return dmRoom, nil
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

func (c *Client) createDM(user string) (string, error) {
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
	return out.RoomID, nil
}

func (c *Client) joinRoom(roomID string) {
	if err := c.do("POST", "/_matrix/client/v3/join/"+url.PathEscape(roomID), map[string]any{}, nil); err != nil {
		log.Printf("join %s: %v", roomID, err)
	}
}

var txnCounter int

func (c *Client) sendHTML(roomID, plain, html string) error {
	txnCounter++
	txn := fmt.Sprintf("ddaybot-%d-%d", time.Now().UnixNano(), txnCounter)
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
