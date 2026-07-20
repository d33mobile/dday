// Matrix client-server transport: login, the raw sync call and its wire types,
// message sending and the generic JSON request helper.

package matrixbot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

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
