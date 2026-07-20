package matrixbot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseAllowedRooms(t *testing.T) {
	got := ParseAllowedRooms(" !a:hs , ,!b:hs,  ")
	want := map[string]bool{"!a:hs": true, "!b:hs": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseAllowedRooms = %v; want %v", got, want)
	}
	if m := ParseAllowedRooms(""); len(m) != 0 {
		t.Errorf("empty input = %v; want empty set", m)
	}
	if m := ParseAllowedRooms("   ,  , "); len(m) != 0 {
		t.Errorf("blank-only input = %v; want empty set", m)
	}
}

func TestRoomAllowed(t *testing.T) {
	c := New("http://x")
	// No allowlist configured: every room is allowed.
	if !c.roomAllowed("!any:hs") {
		t.Error("with no allowlist every room must be allowed")
	}
	c.AllowedRooms = ParseAllowedRooms("!ok:hs")
	if !c.roomAllowed("!ok:hs") {
		t.Error("a listed room must be allowed")
	}
	if c.roomAllowed("!other:hs") {
		t.Error("an unlisted room must be rejected")
	}
}

// TestRunIgnoresDisallowedRoom drives the real Run loop with an allowlist that
// does NOT include the room the "!register" (legacy alias) arrives in, and asserts the bot
// takes no action: no DM is created and nothing is sent.
func TestRunIgnoresDisallowedRoom(t *testing.T) {
	recipient, _ := genKeypair(t)

	var mu sync.Mutex
	var creates, sends int

	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/login", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"access_token": "tok", "user_id": "@ddaybot:mock"})
	})
	mux.HandleFunc("/_matrix/client/v3/sync", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("since") {
		case "":
			writeJSON(w, map[string]any{"next_batch": "s1"})
		case "s1":
			writeJSON(w, map[string]any{
				"next_batch": "s2",
				"rooms": map[string]any{"join": map[string]any{
					"!room:mock": map[string]any{"timeline": map[string]any{"events": []any{
						map[string]any{
							"type": "m.room.message", "sender": "@alice:mock", "event_id": "$e1",
							"content": map[string]any{"msgtype": "m.text", "body": "!register"},
						},
					}}},
				}},
			})
		default:
			time.Sleep(10 * time.Millisecond) // emulate long-poll, avoid busy loop
			writeJSON(w, map[string]any{"next_batch": "s2"})
		}
	})
	mux.HandleFunc("/_matrix/client/v3/createRoom", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		creates++
		mu.Unlock()
		writeJSON(w, map[string]any{"room_id": "!dm:mock"})
	})
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/send/") {
			mu.Lock()
			sends++
			mu.Unlock()
		}
		writeJSON(w, map[string]any{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL)
	c.Recipient = recipient
	c.LinkBase = "https://dday.hs-ldz.pl/register"
	c.TokenSecret = testSecret
	c.AllowedRooms = ParseAllowedRooms("!different:mock") // NOT !room:mock
	if err := c.Login("@ddaybot:mock", "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = c.Run(ctx) }()
	time.Sleep(300 * time.Millisecond) // let Run process the s1 batch
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if creates != 0 {
		t.Errorf("createRoom called %d time(s); want 0 (origin room not on allowlist)", creates)
	}
	if sends != 0 {
		t.Errorf("send called %d time(s); want 0 (origin room not on allowlist)", sends)
	}
}

// TestJoinRoom asserts joinRoom issues a POST to the /join/{roomID} endpoint
// with the room id path-escaped.
func TestJoinRoom(t *testing.T) {
	var mu sync.Mutex
	var joined []string

	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/join/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("join method = %s; want POST", r.Method)
		}
		raw := strings.TrimPrefix(r.URL.Path, "/_matrix/client/v3/join/")
		room, _ := url.PathUnescape(raw)
		mu.Lock()
		joined = append(joined, room)
		mu.Unlock()
		writeJSON(w, map[string]any{"room_id": room})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL)
	c.Token = "tok"
	c.joinRoom("!invite:mock")

	mu.Lock()
	defer mu.Unlock()
	if len(joined) != 1 {
		t.Fatalf("join called %d time(s); want 1", len(joined))
	}
	if joined[0] != "!invite:mock" {
		t.Errorf("joined room = %q; want !invite:mock", joined[0])
	}
}
