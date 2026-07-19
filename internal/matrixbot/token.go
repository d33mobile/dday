package matrixbot

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/agessh"
)

// The registration token carries a JSON RegPayload{handle, issued}, age-encrypted
// to the SSH ed25519 recipient, then standard-base64 encoded. Only the holder of
// the private key can decrypt it, so the link is unforgeable and carries both the
// sender's Matrix handle (used to predefine the nick) and a verifiable issue time.
//
// Conceptually:
//
//	echo '{"h":"@user:hs","t":1234567890}' | age -e -i key | base64 -w 0

// RegPayload is the decrypted content of a registration token: the Matrix handle
// of the sender and the unix time the token was issued.
type RegPayload struct {
	Handle string `json:"h"`
	Issued int64  `json:"t"`
}

// LoadRecipient reads an SSH ed25519 public key file (authorized_keys line) and
// returns it as an age recipient for encryption.
func LoadRecipient(path string) (age.Recipient, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, err := ParseRecipient(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse recipient %s: %w", path, err)
	}
	return r, nil
}

// ParseRecipient parses an SSH ed25519 public key (authorized_keys line) into an
// age recipient. It lets callers supply the key from somewhere other than a file
// (e.g. an env var), sidestepping file-permission issues in containers.
func ParseRecipient(s string) (age.Recipient, error) {
	return agessh.ParseRecipient(strings.TrimSpace(s))
}

// LoadIdentity reads an SSH ed25519 private key file and returns it as an age
// identity for decryption (used to validate/redeem tokens).
func LoadIdentity(path string) (age.Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	id, err := ParseIdentity(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return id, nil
}

// ParseIdentity parses SSH ed25519 private key bytes into an age identity. It
// lets callers supply the key from somewhere other than a file (e.g. an env
// var), sidestepping file-permission issues in containers.
func ParseIdentity(pemBytes []byte) (age.Identity, error) {
	id, err := agessh.ParseIdentity(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("parse identity: %w", err)
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

// EncodeRegToken marshals p to JSON and produces an age-encrypted, base64 token.
func EncodeRegToken(r age.Recipient, p RegPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return EncryptToken(r, string(b))
}

// DecodeRegToken reverses EncodeRegToken using the private identity.
func DecodeRegToken(id age.Identity, token string) (RegPayload, error) {
	plain, err := DecryptToken(id, token)
	if err != nil {
		return RegPayload{}, err
	}
	var p RegPayload
	if err := json.Unmarshal([]byte(plain), &p); err != nil {
		return RegPayload{}, fmt.Errorf("unmarshal payload: %w", err)
	}
	return p, nil
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

// registerLink builds a link for "now" using the client's recipient/base,
// embedding the sender's Matrix handle so the web server can predefine the nick.
func (c *Client) registerLink(handle string) (string, error) {
	if c.Recipient == nil {
		return "", fmt.Errorf("no age recipient configured")
	}
	tok, err := EncodeRegToken(c.Recipient, RegPayload{Handle: handle, Issued: time.Now().Unix()})
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
