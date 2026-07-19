package matrixbot

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"testing"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"golang.org/x/crypto/ssh"
)

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
