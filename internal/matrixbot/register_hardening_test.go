package matrixbot

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestHandleRegisterAlreadyRegistered asserts that when CheckRegistered reports
// the handle is already registered, the bot DMs a "re-registration impossible"
// notice and does NOT hand out a new link (no "?t=").
func TestHandleRegisterAlreadyRegistered(t *testing.T) {
	recipient, _ := genKeypair(t)

	created := make(chan map[string]any, 1)
	sent := make(chan map[string]any, 4)
	srv := newMockMatrix(t, created, sent)
	defer srv.Close()

	c := New(srv.URL)
	c.Recipient = recipient
	c.LinkBase = "https://dday.hs-ldz.pl/register"
	c.CheckRegistered = func(string) (int, bool, error) { return 7, true, nil }
	if err := c.Login("@ddaybot:mock", "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}

	// Empty origin room: no public nudge, only the DM.
	if _, err := c.HandleRegister("", "@alice:mock"); err != nil {
		t.Fatalf("HandleRegister: %v", err)
	}

	sendBody := <-sent
	body, _ := sendBody["body"].(string)
	if !strings.Contains(body, "już zapisany") {
		t.Errorf("body = %q; want 'już zapisany'", body)
	}
	if !strings.Contains(body, "nie jest możliwa") {
		t.Errorf("body = %q; want 'nie jest możliwa'", body)
	}
	if !strings.Contains(body, "#7") {
		t.Errorf("body = %q; want the participant number #7", body)
	}
	if strings.Contains(body, "?t=") {
		t.Errorf("body = %q; must NOT contain a registration link", body)
	}
	if f, _ := sendBody["formatted_body"].(string); strings.Contains(f, "?t=") {
		t.Errorf("formatted_body = %q; must NOT contain a registration link", f)
	}
}

// TestHandleRegisterNudge asserts that a "!register" arriving in a public room
// produces both a DM (with the link) and a public nudge in the origin room that
// @-mentions the user and points them at their DMs.
func TestHandleRegisterNudge(t *testing.T) {
	recipient, _ := genKeypair(t)

	type sendRec struct {
		room string
		body map[string]any
	}
	sends := make(chan sendRec, 4)

	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/login", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"access_token": "tok", "user_id": "@ddaybot:mock"})
	})
	mux.HandleFunc("/_matrix/client/v3/createRoom", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"room_id": "!dm:mock"})
	})
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/send/") {
			sends <- sendRec{room: roomFromPath(r.URL.Path), body: readJSON(t, r)}
		}
		writeJSON(w, map[string]any{"event_id": "$e"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL)
	c.Recipient = recipient
	c.LinkBase = "https://dday.hs-ldz.pl/register"
	if err := c.Login("@ddaybot:mock", "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}

	if _, err := c.HandleRegister("!chan:mock", "@alice:mock"); err != nil {
		t.Fatalf("HandleRegister: %v", err)
	}

	var dm, nudge sendRec
	for i := 0; i < 2; i++ {
		select {
		case s := <-sends:
			if s.room == "!chan:mock" {
				nudge = s
			} else {
				dm = s
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for send %d/2", i+1)
		}
	}

	if nudge.room != "!chan:mock" {
		t.Fatalf("expected a public nudge in !chan:mock, got none")
	}
	nb, _ := nudge.body["body"].(string)
	if !strings.Contains(nb, "sprawdź prywatne wiadomości") {
		t.Errorf("nudge body = %q; want 'sprawdź prywatne wiadomości'", nb)
	}
	nf, _ := nudge.body["formatted_body"].(string)
	if !strings.Contains(nf, "matrix.to/#/@alice:mock") {
		t.Errorf("nudge formatted_body = %q; want a matrix.to mention of the user", nf)
	}

	db, _ := dm.body["body"].(string)
	if !strings.Contains(db, "?t=") {
		t.Errorf("dm body = %q; want the registration link", db)
	}
}

// roomFromPath pulls the room id out of a /_matrix/.../rooms/{roomID}/send/...
// path, undoing the path escaping the client applies.
func roomFromPath(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == "rooms" && i+1 < len(parts) {
			room, _ := url.PathUnescape(parts[i+1])
			return room
		}
	}
	return ""
}
