package main

import (
	"crypto/ed25519"
	"crypto/rand"
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

// testTokenSecret is the shared HMAC key both the test token helpers and the
// mux under test use, so tokens the tests mint verify in decode().
const testTokenSecret = "web-token-secret"

// testEnv wires an ephemeral store, keypair and mux together.
type testEnv struct {
	recipient age.Recipient
	store     *store.Store
	handler   http.Handler
}

func newTestEnv(t *testing.T, open bool, limit int) *testEnv {
	return newTestEnvToken(t, open, limit, "")
}

// newTestEnvToken is newTestEnv with an explicit internal token, so tests can
// exercise the guarded /api/registered endpoint (empty token disables it). It
// uses no waiting-list capacity, so total == seatLimit and the classic full
// behavior applies.
func newTestEnvToken(t *testing.T, open bool, limit int, internalToken string) *testEnv {
	return newTestEnvSeats(t, open, limit, 0, internalToken)
}

// newTestEnvSeats builds the mux with an explicit confirmed-seat limit and
// waiting-list limit, so tests can exercise the two-tier capacity model.
func newTestEnvSeats(t *testing.T, open bool, seatLimit, waitlistLimit int, internalToken string) *testEnv {
	t.Helper()
	return newTestEnvFull(t, open, seatLimit, waitlistLimit, internalToken, "")
}

// newTestEnvAdmin builds the mux with an explicit admin token, so the /admin
// tests can exercise the guarded view (empty token disables it).
func newTestEnvAdmin(t *testing.T, seatLimit, waitlistLimit int, adminToken string) *testEnv {
	t.Helper()
	return newTestEnvFull(t, true, seatLimit, waitlistLimit, "", adminToken)
}

// newTestEnvFull is the single mux builder every helper above delegates to.
func newTestEnvFull(t *testing.T, open bool, seatLimit, waitlistLimit int, internalToken, adminToken string) *testEnv {
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
		store:         st,
		identity:      id,
		seatLimit:     seatLimit,
		waitlistLimit: waitlistLimit,
		isOpen:        func() bool { return open },
		files:         http.FS(sub),
		internalToken: internalToken,
		adminToken:    adminToken,
		tokenSecret:   testTokenSecret,
	})
	return &testEnv{recipient: rcpt, store: st, handler: h}
}

func (e *testEnv) token(t *testing.T, handle string) string {
	t.Helper()
	return e.tokenAt(t, handle, time.Now().Unix())
}

// tokenAt mints a registration token with an explicit Issued time, so tests can
// exercise the TTL (expired / future-dated links).
func (e *testEnv) tokenAt(t *testing.T, handle string, issued int64) string {
	t.Helper()
	return e.tokenKindAt(t, handle, matrixbot.KindReg, issued)
}

// tokenKind mints a token of the given kind (matrixbot.KindReg / KindPanel) for
// "now", so tests can check that each endpoint only accepts its own kind.
func (e *testEnv) tokenKind(t *testing.T, handle, kind string) string {
	t.Helper()
	return e.tokenKindAt(t, handle, kind, time.Now().Unix())
}

func (e *testEnv) tokenKindAt(t *testing.T, handle, kind string, issued int64) string {
	t.Helper()
	tok, err := matrixbot.EncodeRegToken(e.recipient, testTokenSecret,
		matrixbot.RegPayload{Handle: handle, Issued: issued, Kind: kind})
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
	return e.postTo(t, "/register", form)
}

func (e *testEnv) postTo(t *testing.T, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}
