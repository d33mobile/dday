package matrixbot

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/agessh"
)

// The registration token replicates this shell pipeline:
//
//	date +%s | age -e -i ~/.ssh/id_ed25519 -o - | base64 -w 0
//
// i.e. the current unix timestamp, age-encrypted to the SSH ed25519 recipient,
// then standard-base64 encoded. Only the holder of the private key can decrypt
// it, so the link is unforgeable and carries a verifiable issue time.

// LoadRecipient reads an SSH ed25519 public key file (authorized_keys line) and
// returns it as an age recipient for encryption.
func LoadRecipient(path string) (age.Recipient, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, err := agessh.ParseRecipient(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("parse recipient %s: %w", path, err)
	}
	return r, nil
}

// LoadIdentity reads an SSH ed25519 private key file and returns it as an age
// identity for decryption (used to validate/redeem tokens).
func LoadIdentity(path string) (age.Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	id, err := agessh.ParseIdentity(data)
	if err != nil {
		return nil, fmt.Errorf("parse identity %s: %w", path, err)
	}
	return id, nil
}

// EncryptToken age-encrypts plaintext to r and returns standard base64.
func EncryptToken(r age.Recipient, plaintext string) (string, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, r)
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(w, plaintext); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// DecryptToken reverses EncryptToken using the private identity.
func DecryptToken(id age.Identity, token string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return "", fmt.Errorf("base64: %w", err)
	}
	r, err := age.Decrypt(bytes.NewReader(raw), id)
	if err != nil {
		return "", fmt.Errorf("age decrypt: %w", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// RegisterLink appends the token to the base URL as a `t` query parameter.
func RegisterLink(base, token string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	// The token is standard base64 (may contain +, /, =); query-escape it.
	return base + sep + "t=" + queryEscape(token)
}

// registerLink builds a link for "now" using the client's recipient/base.
func (c *Client) registerLink() (string, error) {
	if c.Recipient == nil {
		return "", fmt.Errorf("no age recipient configured")
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	tok, err := EncryptToken(c.Recipient, ts)
	if err != nil {
		return "", err
	}
	return RegisterLink(c.LinkBase, tok), nil
}

// queryEscape is url.QueryEscape without importing net/url here.
func queryEscape(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			b.WriteByte(ch)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperhex[ch>>4])
		b.WriteByte(upperhex[ch&0x0f])
	}
	return b.String()
}
