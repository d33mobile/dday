package matrixbot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestRegisterFlowEndToEnd drives the real Client.Run loop against a mock
// Matrix homeserver and asserts the full "!start" flow:
//
//	login -> sync -> see !start from a user -> create a direct (DM) room
//	inviting that user -> send a message containing a registration link whose
//	token decrypts (with the private key) back to the sender's handle plus a
//	fresh unix timestamp.
func TestRegisterFlowEndToEnd(t *testing.T) {
	recipient, identity := genKeypair(t)

	const botMXID = "@ddaybot:mock"
	const userMXID = "@alice:mock"

	created := make(chan map[string]any, 1)
	// Buffered for two sends: the DM (with the link) and the public nudge that
	// Run posts back in the origin room.
	sent := make(chan map[string]any, 4)

	mux := http.NewServeMux()

	mux.HandleFunc("/_matrix/client/v3/login", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"access_token": "tok", "user_id": botMXID})
	})

	// sync state machine: prime -> deliver !start once -> quiet.
	mux.HandleFunc("/_matrix/client/v3/sync", func(w http.ResponseWriter, r *http.Request) {
		since := r.URL.Query().Get("since")
		switch since {
		case "":
			writeJSON(w, map[string]any{"next_batch": "s1"})
		case "s1":
			writeJSON(w, map[string]any{
				"next_batch": "s2",
				"rooms": map[string]any{
					"join": map[string]any{
						"!room:mock": map[string]any{
							"timeline": map[string]any{
								"events": []any{
									map[string]any{
										"type":     "m.room.message",
										"sender":   userMXID,
										"event_id": "$e1",
										"content":  map[string]any{"msgtype": "m.text", "body": "!start"},
									},
								},
							},
						},
					},
				},
			})
		default:
			time.Sleep(20 * time.Millisecond) // emulate long-poll, avoid busy loop
			writeJSON(w, map[string]any{"next_batch": "s2"})
		}
	})

	mux.HandleFunc("/_matrix/client/v3/createRoom", func(w http.ResponseWriter, r *http.Request) {
		created <- readJSON(t, r)
		writeJSON(w, map[string]any{"room_id": "!dm:mock"})
	})

	// PUT /rooms/{id}/send/m.room.message/{txn}
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/send/") {
			sent <- readJSON(t, r)
			writeJSON(w, map[string]any{"event_id": "$sent"})
			return
		}
		writeJSON(w, map[string]any{})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL)
	c.Recipient = recipient
	c.LinkBase = "https://dday.hs-ldz.pl/register"
	c.TokenSecret = testSecret
	if err := c.Login(botMXID, "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	// The bot must open a DM inviting the user, marked is_direct.
	var createBody map[string]any
	select {
	case createBody = <-created:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for createRoom")
	}
	if d, _ := createBody["is_direct"].(bool); !d {
		t.Errorf("createRoom is_direct = %v, want true", createBody["is_direct"])
	}
	if !inviteContains(createBody["invite"], userMXID) {
		t.Errorf("createRoom invite = %v, want to include %s", createBody["invite"], userMXID)
	}

	// The bot must send a message carrying a valid registration link.
	var sendBody map[string]any
	select {
	case sendBody = <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for send")
	}
	body, _ := sendBody["body"].(string)
	link := lastField(body)
	if link == "" {
		t.Fatalf("no link in message body: %q", body)
	}

	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link %q: %v", link, err)
	}
	token := u.Query().Get("t")
	if token == "" {
		t.Fatalf("no token in link %q", link)
	}

	payload, err := DecodeRegToken(identity, testSecret, token)
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if payload.Handle != userMXID {
		t.Fatalf("token handle = %q, want %q", payload.Handle, userMXID)
	}
	if delta := time.Now().Unix() - payload.Issued; delta < 0 || delta > 300 {
		t.Fatalf("token issued %d not fresh (delta %ds)", payload.Issued, delta)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	data, _ := io.ReadAll(r.Body)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("bad request body %q: %v", string(data), err)
	}
	return m
}

func inviteContains(invite any, mxid string) bool {
	list, ok := invite.([]any)
	if !ok {
		return false
	}
	for _, v := range list {
		if s, _ := v.(string); s == mxid {
			return true
		}
	}
	return false
}

func lastField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}
