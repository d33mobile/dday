package matrixbot

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestIsStartCmd pins down which messages trigger the bot: "!start" is the
// official command and "!register" is kept as a legacy alias, both
// case-insensitively and regardless of trailing words. Anything else — another
// bang-command, the word embedded mid-sentence, an empty body — is ignored.
func TestIsStartCmd(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"!start", true},
		{"  !start  ", true},
		{"!START", true},
		{"!Start please", true},
		{"!register", true},
		{"!REGISTER", true},
		{"!register now", true},
		{"!stop", false},
		{"!starting", false},
		{"!startup", false},
		{"start", false},
		{"hej, napisz !start do bota", false},
		{"", false},
		{"   ", false},
	}
	for _, tc := range cases {
		if got := isStartCmd(tc.body); got != tc.want {
			t.Errorf("isStartCmd(%q) = %v; want %v", tc.body, got, tc.want)
		}
	}
}

// TestHandleRegisterAlreadyRegisteredSendsPanelLink asserts the already-signed-up
// branch: with PanelBase configured the bot does NOT publicly refuse. It DMs the
// user a magic link to their panel — whose token must decode to KindPanel for
// that very handle — and leaves only a public nudge in the origin room. The
// nudge must not carry the link, and the participant number must stay out of the
// channel.
func TestHandleRegisterAlreadyRegisteredSendsPanelLink(t *testing.T) {
	recipient, identity := genKeypair(t)

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
	mux.HandleFunc("/_matrix/client/v3/user/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, `{"errcode":"M_NOT_FOUND"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{})
	})
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/send/"):
			sends <- sendRec{room: roomFromPath(r.URL.Path), body: readJSON(t, r)}
			writeJSON(w, map[string]any{"event_id": "$e"})
		case strings.Contains(r.URL.Path, "/joined_members"):
			// A three-member room, so the origin is never mistaken for a 1:1 DM
			// and the public nudge branch stands.
			writeJSON(w, map[string]any{"joined": map[string]any{
				"@ddaybot:mock": map[string]any{},
				"@alice:mock":   map[string]any{},
				"@other:mock":   map[string]any{},
			}})
		default:
			writeJSON(w, map[string]any{})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL)
	c.Recipient = recipient
	c.LinkBase = "https://dday.hs-ldz.pl/register"
	c.PanelBase = "https://dday.hs-ldz.pl/panel"
	c.TokenSecret = testSecret
	c.CheckRegistered = func(string) (int, bool, error) { return 7, true, nil }
	if err := c.Login("@ddaybot:mock", "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}

	room, err := c.HandleRegister("!chan:mock", "@alice:mock")
	if err != nil {
		t.Fatalf("HandleRegister: %v", err)
	}
	if room != "!dm:mock" {
		t.Errorf("HandleRegister returned room %q; want the DM room", room)
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

	// The DM carries the panel magic link and (privately) the participant number.
	db, _ := dm.body["body"].(string)
	if !strings.Contains(db, "https://dday.hs-ldz.pl/panel?t=") {
		t.Fatalf("dm body = %q; want a /panel?t= magic link", db)
	}
	if !strings.Contains(db, "#7") {
		t.Errorf("dm body = %q; want the participant number", db)
	}
	if strings.Contains(db, "nie jest możliwa") {
		t.Errorf("dm body = %q; must not be the old refusal", db)
	}
	if df, _ := dm.body["formatted_body"].(string); !strings.Contains(df, `<a href="https://dday.hs-ldz.pl/panel?t=`) {
		t.Errorf("dm formatted_body = %q; want an <a href> to the panel", df)
	}

	// The token in that link must be a panel token for exactly this handle.
	payload, err := DecodeRegToken(identity, testSecret, tokenFromLink(t, db))
	if err != nil {
		t.Fatalf("DecodeRegToken: %v", err)
	}
	if payload.Kind != KindPanel {
		t.Errorf("token kind = %q; want %q", payload.Kind, KindPanel)
	}
	if payload.Handle != "@alice:mock" {
		t.Errorf("token handle = %q; want @alice:mock", payload.Handle)
	}

	// The channel gets a nudge only: no link, no participant number.
	nb, _ := nudge.body["body"].(string)
	if !strings.Contains(nb, "sprawdź prywatne wiadomości") {
		t.Errorf("nudge body = %q; want 'sprawdź prywatne wiadomości'", nb)
	}
	if !strings.Contains(nb, "link do Twojego panelu") {
		t.Errorf("nudge body = %q; want it to mention the panel link", nb)
	}
	if strings.Contains(nb, "?t=") || strings.Contains(nb, "#7") {
		t.Errorf("nudge body = %q; must leak neither the link nor the number", nb)
	}
	if nf, _ := nudge.body["formatted_body"].(string); strings.Contains(nf, "?t=") {
		t.Errorf("nudge formatted_body = %q; must NOT contain the magic link", nf)
	}
}

// tokenFromLink pulls the `t` query parameter out of the first URL found in a
// message body, undoing the query escaping applied when the link was built.
func tokenFromLink(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "https://")
	if i < 0 {
		t.Fatalf("no link in %q", body)
	}
	raw := strings.Fields(body[i:])[0]
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse link %q: %v", raw, err)
	}
	tok := u.Query().Get("t")
	if tok == "" {
		t.Fatalf("link %q carries no t= token", raw)
	}
	return tok
}
