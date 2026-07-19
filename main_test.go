package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/d33mobile/dday/internal/matrixbot"
	"github.com/d33mobile/dday/internal/store"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"golang.org/x/crypto/ssh"
)

// genKeypair creates an in-memory SSH ed25519 keypair and returns the matching
// age recipient (encrypt) and identity (decrypt).
func genKeypair(t *testing.T) (age.Recipient, age.Identity) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "dday-test")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	id, err := agessh.ParseIdentity(pem.EncodeToMemory(block))
	if err != nil {
		t.Fatalf("parse identity: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("new public key: %v", err)
	}
	r, err := agessh.ParseRecipient(string(ssh.MarshalAuthorizedKey(sshPub)))
	if err != nil {
		t.Fatalf("parse recipient: %v", err)
	}
	return r, id
}

// testEnv wires an ephemeral store, keypair and mux together.
type testEnv struct {
	recipient age.Recipient
	store     *store.Store
	handler   http.Handler
}

func newTestEnv(t *testing.T, open bool, limit int) *testEnv {
	t.Helper()
	rcpt, id := genKeypair(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	sub, err := fs.Sub(embedded, ".")
	if err != nil {
		t.Fatalf("embed sub: %v", err)
	}

	h := newMux(deps{
		store:     st,
		identity:  id,
		seatLimit: limit,
		isOpen:    func() bool { return open },
		files:     http.FS(sub),
	})
	return &testEnv{recipient: rcpt, store: st, handler: h}
}

func (e *testEnv) token(t *testing.T, handle string) string {
	t.Helper()
	tok, err := matrixbot.EncodeRegToken(e.recipient, matrixbot.RegPayload{Handle: handle, Issued: time.Now().Unix()})
	if err != nil {
		t.Fatalf("encode token: %v", err)
	}
	return tok
}

func (e *testEnv) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func (e *testEnv) post(t *testing.T, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func TestRegisterGetShowsForm(t *testing.T) {
	e := newTestEnv(t, true, 20)
	tok := e.token(t, "@alice:hs.org")

	rec := e.get(t, "/register?t="+url.QueryEscape(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alice") {
		t.Errorf("body missing nick 'alice'")
	}
	if !strings.Contains(body, `name="email"`) {
		t.Errorf("body missing email field")
	}
	if !strings.Contains(body, "/privacy") {
		t.Errorf("body missing privacy link")
	}
}

func TestRegisterPostSuccess(t *testing.T) {
	e := newTestEnv(t, true, 20)
	tok := e.token(t, "@alice:hs.org")

	rec := e.post(t, url.Values{"t": {tok}, "city": {"Łódź"}, "email": {"alice@example.com"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "#1") {
		t.Errorf("body missing participant number #1: %s", rec.Body.String())
	}
	if n, _ := e.store.Count(); n != 1 {
		t.Fatalf("count = %d; want 1", n)
	}

	// /api/count reflects the new state.
	crec := e.get(t, "/api/count")
	if ct := crec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q; want json", ct)
	}
	var got struct {
		Count int  `json:"count"`
		Limit int  `json:"limit"`
		Open  bool `json:"open"`
	}
	if err := json.Unmarshal(crec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode count: %v", err)
	}
	if got.Count != 1 || got.Limit != 20 || !got.Open {
		t.Errorf("api/count = %+v; want {1 20 true}", got)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	e := newTestEnv(t, true, 20)
	tok := e.token(t, "@alice:hs.org")

	if rec := e.post(t, url.Values{"t": {tok}, "city": {"Łódź"}, "email": {"a@example.com"}}); rec.Code != http.StatusOK {
		t.Fatalf("first post status = %d", rec.Code)
	}
	rec := e.post(t, url.Values{"t": {tok}, "city": {"Kraków"}, "email": {"a@example.com"}})
	body := rec.Body.String()
	if !strings.Contains(body, "Jesteś już zapisany") {
		t.Errorf("duplicate body missing 'już zapisany': %s", body)
	}
	if n, _ := e.store.Count(); n != 1 {
		t.Fatalf("count = %d; want 1 after duplicate", n)
	}
}

func TestRegisterFull(t *testing.T) {
	e := newTestEnv(t, true, 2)
	for _, h := range []string{"@a:hs.org", "@b:hs.org"} {
		tok := e.token(t, h)
		if rec := e.post(t, url.Values{"t": {tok}, "city": {"Łódź"}, "email": {"x@example.com"}}); rec.Code != http.StatusOK {
			t.Fatalf("post %s status = %d", h, rec.Code)
		}
	}
	// Third distinct handle exceeds the limit.
	tok := e.token(t, "@c:hs.org")
	rec := e.post(t, url.Values{"t": {tok}, "city": {"Łódź"}, "email": {"c@example.com"}})
	if !strings.Contains(rec.Body.String(), "Brak miejsc") {
		t.Errorf("full body missing 'Brak miejsc': %s", rec.Body.String())
	}
	if n, _ := e.store.Count(); n != 2 {
		t.Fatalf("count = %d; want 2", n)
	}

	// GET should also advertise no seats.
	grec := e.get(t, "/register?t="+url.QueryEscape(tok))
	if !strings.Contains(grec.Body.String(), "Brak miejsc") {
		t.Errorf("full GET missing 'Brak miejsc'")
	}
}

func TestRegisterClosed(t *testing.T) {
	e := newTestEnv(t, false, 20)
	tok := e.token(t, "@alice:hs.org")

	grec := e.get(t, "/register?t="+url.QueryEscape(tok))
	if !strings.Contains(grec.Body.String(), "nieotwarte") {
		t.Errorf("closed GET missing 'nieotwarte': %s", grec.Body.String())
	}

	prec := e.post(t, url.Values{"t": {tok}, "city": {"Łódź"}, "email": {"a@example.com"}})
	if !strings.Contains(prec.Body.String(), "nieotwarte") {
		t.Errorf("closed POST missing 'nieotwarte'")
	}
	if n, _ := e.store.Count(); n != 0 {
		t.Fatalf("count = %d; want 0 when closed", n)
	}

	// /api/count reports open=false.
	var got struct {
		Open bool `json:"open"`
	}
	_ = json.Unmarshal(e.get(t, "/api/count").Body.Bytes(), &got)
	if got.Open {
		t.Errorf("api/count open = true; want false")
	}
}

func TestRegisterBadToken(t *testing.T) {
	e := newTestEnv(t, true, 20)

	if rec := e.get(t, "/register?t=not-a-valid-token"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad token GET status = %d; want 400", rec.Code)
	}
	if rec := e.get(t, "/register"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing token GET status = %d; want 400", rec.Code)
	}
	if n, _ := e.store.Count(); n != 0 {
		t.Fatalf("count = %d; want 0", n)
	}
}

func TestRegisterInvalidEmail(t *testing.T) {
	e := newTestEnv(t, true, 20)
	tok := e.token(t, "@alice:hs.org")

	rec := e.post(t, url.Values{"t": {tok}, "city": {"Łódź"}, "email": {"not-an-email"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (re-render)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "poprawny adres e-mail") {
		t.Errorf("invalid email body missing error: %s", body)
	}
	if !strings.Contains(body, `name="email"`) {
		t.Errorf("invalid email should re-render form")
	}
	if n, _ := e.store.Count(); n != 0 {
		t.Fatalf("count = %d; want 0 after invalid email", n)
	}
}

// TestRegistrationDisabled verifies that when the age key / store are missing
// the site still serves the landing (and privacy), and registration degrades to
// 503 instead of crashing the server.
func TestRegistrationDisabled(t *testing.T) {
	sub, err := fs.Sub(embedded, ".")
	if err != nil {
		t.Fatalf("embed sub: %v", err)
	}
	h := newMux(deps{
		store:     nil,
		identity:  nil,
		seatLimit: 20,
		isOpen:    func() bool { return true },
		files:     http.FS(sub),
	})

	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}

	if rec := do(http.MethodGet, "/"); rec.Code != http.StatusOK {
		t.Errorf("GET / = %d; want 200 (landing must always serve)", rec.Code)
	}
	if rec := do(http.MethodGet, "/privacy"); rec.Code != http.StatusOK {
		t.Errorf("GET /privacy = %d; want 200", rec.Code)
	}
	if rec := do(http.MethodGet, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("GET /healthz = %d; want 200", rec.Code)
	}
	if rec := do(http.MethodGet, "/register?t=x"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /register = %d; want 503", rec.Code)
	}
	// /api/count still answers, reporting zero.
	crec := do(http.MethodGet, "/api/count")
	if crec.Code != http.StatusOK {
		t.Fatalf("GET /api/count = %d; want 200", crec.Code)
	}
	var got struct {
		Count int `json:"count"`
		Limit int `json:"limit"`
	}
	if err := json.Unmarshal(crec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode count: %v", err)
	}
	if got.Count != 0 || got.Limit != 20 {
		t.Errorf("api/count = %+v; want count 0 limit 20", got)
	}
}

// TestRegisterFlowBotToWeb is the full cross-component e2e: the real bot
// (matrixbot.Client.HandleRegister) encrypts a link to the same key the real
// web handler decrypts, and the link the bot ACTUALLY emits is driven through
// GET+POST /register to a stored, numbered registration. It closes the gap
// between the bot-side and web-side tests, which otherwise only meet at the
// token format.
func TestRegisterFlowBotToWeb(t *testing.T) {
	// Web side: real handler + store, with an ephemeral keypair.
	e := newTestEnv(t, true, 20)

	// Mock Matrix homeserver to receive the bot's createRoom + send.
	sent := make(chan string, 1)
	mm := http.NewServeMux()
	mm.HandleFunc("/_matrix/client/v3/createRoom", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"room_id":"!dm:mock"}`))
	})
	mm.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/send/") {
			var body struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			sent <- body.Body
		}
		_, _ = w.Write([]byte(`{"event_id":"$e"}`))
	})
	ms := httptest.NewServer(mm)
	defer ms.Close()

	// Bot side: real Client encrypting to the SAME recipient the web decrypts.
	bot := matrixbot.New(ms.URL)
	bot.Token = "tok"
	bot.Recipient = e.recipient
	bot.LinkBase = "https://dday.hs-ldz.pl/register"

	if _, err := bot.HandleRegister("@alice:hs.org"); err != nil {
		t.Fatalf("bot HandleRegister: %v", err)
	}

	var msg string
	select {
	case msg = <-sent:
	case <-time.After(3 * time.Second):
		t.Fatal("bot did not send a message")
	}

	// Pull the token out of the link the bot actually emitted.
	fields := strings.Fields(msg)
	if len(fields) == 0 {
		t.Fatalf("empty bot message")
	}
	link := fields[len(fields)-1]
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse bot link %q: %v", link, err)
	}
	token := u.Query().Get("t")
	if token == "" {
		t.Fatalf("no token in bot link %q", link)
	}

	// GET with the bot's token renders the form pre-filled with the nick.
	grec := e.get(t, "/register?t="+url.QueryEscape(token))
	if grec.Code != http.StatusOK || !strings.Contains(grec.Body.String(), "alice") {
		t.Fatalf("GET with bot token: code=%d, missing nick 'alice'", grec.Code)
	}

	// POST the bot's token registers the participant.
	prec := e.post(t, url.Values{"t": {token}, "city": {"Łódź"}, "email": {"alice@example.com"}})
	if prec.Code != http.StatusOK {
		t.Fatalf("register status = %d", prec.Code)
	}
	if !strings.Contains(prec.Body.String(), "#1") {
		t.Errorf("expected participant number #1, body: %s", prec.Body.String())
	}
	if n, _ := e.store.Count(); n != 1 {
		t.Fatalf("count = %d; want 1", n)
	}
}

func TestPrivacyPage(t *testing.T) {
	e := newTestEnv(t, true, 20)
	rec := e.get(t, "/privacy")
	if rec.Code != http.StatusOK {
		t.Fatalf("privacy status = %d; want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Administratorem danych") {
		t.Errorf("privacy body missing RODO content")
	}
}
