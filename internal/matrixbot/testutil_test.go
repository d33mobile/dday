package matrixbot

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"golang.org/x/crypto/ssh"
)

// newMockMatrix stands up a minimal mock Matrix homeserver that answers login,
// createRoom, and room send. Bodies of createRoom / send are forwarded to the
// created / sent channels (buffered by the caller) for assertions. sync is not
// registered here — tests that drive Client.Run supply their own.
func newMockMatrix(t *testing.T, created, sent chan map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/login", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"access_token": "tok", "user_id": "@ddaybot:mock"})
	})
	mux.HandleFunc("/_matrix/client/v3/createRoom", func(w http.ResponseWriter, r *http.Request) {
		created <- readJSON(t, r)
		writeJSON(w, map[string]any{"room_id": "!dm:mock"})
	})
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/send/") {
			sent <- readJSON(t, r)
			writeJSON(w, map[string]any{"event_id": "$sent"})
			return
		}
		writeJSON(w, map[string]any{})
	})
	return httptest.NewServer(mux)
}

// genKeypair creates an in-memory SSH ed25519 keypair and returns the matching
// age recipient (from the public key) and identity (from the private key),
// mirroring the on-disk config/*_ed25519[.pub] pair used in production.
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
