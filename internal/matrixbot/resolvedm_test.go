package matrixbot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRegisterInExistingDMRepliesThere asserts that a "!register" arriving in a
// room that is already a 1:1 DM with the user (joined_members == {bot, user}) is
// answered in that very room: the bot must NOT create a new room and must NOT
// post a public nudge (there is no separate channel to point back to).
func TestRegisterInExistingDMRepliesThere(t *testing.T) {
	recipient, _ := genKeypair(t)

	const bot = "@ddaybot:mock"
	const user = "@alice:mock"
	const dm = "!existingdm:mock"

	created := make(chan map[string]any, 4)
	type sendRec struct {
		room string
		body map[string]any
	}
	sends := make(chan sendRec, 4)

	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/login", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"access_token": "tok", "user_id": bot})
	})
	mux.HandleFunc("/_matrix/client/v3/createRoom", func(w http.ResponseWriter, r *http.Request) {
		created <- readJSON(t, r)
		writeJSON(w, map[string]any{"room_id": "!new:mock"})
	})
	mux.HandleFunc("/_matrix/client/v3/user/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, `{"errcode":"M_NOT_FOUND"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{})
	})
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/joined_members"):
			// The origin room is a 1:1 with the user.
			writeJSON(w, map[string]any{"joined": map[string]any{
				bot:  map[string]any{},
				user: map[string]any{},
			}})
		case strings.Contains(r.URL.Path, "/send/"):
			sends <- sendRec{room: roomFromPath(r.URL.Path), body: readJSON(t, r)}
			writeJSON(w, map[string]any{"event_id": "$e"})
		default:
			writeJSON(w, map[string]any{})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL)
	c.Recipient = recipient
	c.LinkBase = "https://dday.hs-ldz.pl/register"
	c.TokenSecret = testSecret
	if err := c.Login(bot, "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}

	room, err := c.HandleRegister(dm, user)
	if err != nil {
		t.Fatalf("HandleRegister: %v", err)
	}
	if room != dm {
		t.Errorf("HandleRegister room = %q; want the existing DM %q", room, dm)
	}

	// Exactly one send, into the existing DM, carrying the link.
	select {
	case s := <-sends:
		if s.room != dm {
			t.Errorf("send room = %q; want the existing DM %q", s.room, dm)
		}
		if b, _ := s.body["body"].(string); !strings.Contains(b, "?t=") {
			t.Errorf("send body = %q; want the registration link", b)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the reply send")
	}

	// No second send (the nudge must be skipped) and no room was created.
	select {
	case s := <-sends:
		t.Errorf("unexpected second send to %q; a nudge must be skipped in an existing DM", s.room)
	case <-time.After(200 * time.Millisecond):
	}
	if n := len(created); n != 0 {
		t.Errorf("createRoom calls = %d; want 0 (existing DM reused)", n)
	}
}

// TestExistingOrNewDMReusesMDirect asserts that when the m.direct account data
// already records a DM room for the user (e.g. after a bot restart, before the
// in-memory cache is warm), existingOrNewDM returns that room without POSTing
// /createRoom, and warms the cache with it.
func TestExistingOrNewDMReusesMDirect(t *testing.T) {
	const bot = "@ddaybot:mock"
	const user = "@alice:mock"
	const dm = "!frommdirect:mock"

	created := make(chan map[string]any, 4)

	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/login", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"access_token": "tok", "user_id": bot})
	})
	mux.HandleFunc("/_matrix/client/v3/createRoom", func(w http.ResponseWriter, r *http.Request) {
		created <- readJSON(t, r)
		writeJSON(w, map[string]any{"room_id": "!new:mock"})
	})
	mux.HandleFunc("/_matrix/client/v3/user/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, map[string]any{user: []string{dm}})
			return
		}
		writeJSON(w, map[string]any{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login(bot, "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}

	room, err := c.existingOrNewDM(user)
	if err != nil {
		t.Fatalf("existingOrNewDM: %v", err)
	}
	if room != dm {
		t.Errorf("existingOrNewDM room = %q; want the m.direct room %q", room, dm)
	}
	if n := len(created); n != 0 {
		t.Errorf("createRoom calls = %d; want 0 (reused from m.direct)", n)
	}
	if got := c.dmRooms[user]; got != dm {
		t.Errorf("dmRooms[%s] = %q; want the room warmed into cache %q", user, got, dm)
	}
}
