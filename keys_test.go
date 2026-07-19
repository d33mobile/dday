package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/d33mobile/dday/internal/matrixbot"

	"golang.org/x/crypto/ssh"
)

// genKeyMaterial returns a fresh SSH ed25519 keypair as the two on-disk
// representations production consumes: the private key PEM and the public
// authorized_keys line.
func genKeyMaterial(t *testing.T) (privPEM, pubLine []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "dday-test")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("new public key: %v", err)
	}
	return pem.EncodeToMemory(block), ssh.MarshalAuthorizedKey(sshPub)
}

// TestLoadIdentityFromEnv verifies loadIdentity parses the base64 AGE_KEY_DATA
// private key, and that the resulting identity decrypts what the matching
// recipient encrypts (round-trip).
func TestLoadIdentityFromEnv(t *testing.T) {
	privPEM, pubLine := genKeyMaterial(t)
	t.Setenv("AGE_KEY_DATA", base64.StdEncoding.EncodeToString(privPEM))

	id, err := loadIdentity()
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}

	r, err := matrixbot.ParseRecipient(string(pubLine))
	if err != nil {
		t.Fatalf("parse recipient: %v", err)
	}
	tok, err := matrixbot.EncryptToken(r, "hello")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := matrixbot.DecryptToken(id, tok)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "hello" {
		t.Errorf("round-trip = %q; want hello", got)
	}
}

func TestLoadIdentityBadBase64(t *testing.T) {
	t.Setenv("AGE_KEY_DATA", "!!! not base64 !!!")
	if _, err := loadIdentity(); err == nil {
		t.Fatal("expected an error for invalid AGE_KEY_DATA base64")
	}
}

// TestLoadKeysFromFile writes a keypair to disk and checks matrixbot.LoadIdentity
// / LoadRecipient parse the files, with an EncodeRegToken -> DecodeRegToken
// round-trip (including the HMAC layer) proving the two halves match.
func TestLoadKeysFromFile(t *testing.T) {
	privPEM, pubLine := genKeyMaterial(t)
	dir := t.TempDir()
	privPath := filepath.Join(dir, "id")
	pubPath := filepath.Join(dir, "id.pub")
	if err := os.WriteFile(privPath, privPEM, 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	if err := os.WriteFile(pubPath, pubLine, 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}

	id, err := matrixbot.LoadIdentity(privPath)
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	r, err := matrixbot.LoadRecipient(pubPath)
	if err != nil {
		t.Fatalf("LoadRecipient: %v", err)
	}

	const secret = "shared-secret"
	want := matrixbot.RegPayload{Handle: "@alice:hs.org", Issued: 1234567890}
	tok, err := matrixbot.EncodeRegToken(r, secret, want)
	if err != nil {
		t.Fatalf("encode token: %v", err)
	}
	got, err := matrixbot.DecodeRegToken(id, secret, tok)
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if got.Handle != want.Handle || got.Issued != want.Issued {
		t.Errorf("round-trip = %+v; want %+v", got, want)
	}
}

func TestLoadRecipientFromFileMissing(t *testing.T) {
	if _, err := matrixbot.LoadRecipient(filepath.Join(t.TempDir(), "absent.pub")); err == nil {
		t.Error("expected an error loading a missing recipient file")
	}
}
