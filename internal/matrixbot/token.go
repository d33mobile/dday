package matrixbot

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/agessh"
)

// The registration token carries a JSON RegPayload{handle, issued, mac},
// age-encrypted to the SSH ed25519 recipient, then standard-base64 encoded.
//
// Two independent layers protect the token:
//
//   - Confidentiality comes from age: only the holder of the private key can
//     decrypt the payload.
//   - Authenticity comes from an HMAC-SHA256 over (handle, issued, kind) keyed
//     by the shared TOKEN_SECRET. On decode we recompute the MAC and compare it
//     constant-time; a wrong or missing MAC is rejected. Because the kind is
//     covered by the MAC, a registration token cannot be re-labelled as a panel
//     token (or vice versa) without the secret.
//
// The age recipient (the SSH .pub key) is only needed to ENCRYPT to the web
// server, so it is not a secret — leaking it does NOT let an attacker forge a
// token, because forging still requires the HMAC key (TOKEN_SECRET). An empty
// TOKEN_SECRET degrades to no real authenticity (both bot and web must agree);
// `make up` always generates one, so production is authenticated by default.
//
// Conceptually:
//
//	echo '{"h":"@user:hs","t":1234567890,"m":"<hmac>"}' | age -e -i key | base64 -w 0

// Token kinds. The kind scopes a token to one endpoint: a registration link
// cannot open the participant panel and a panel magic link cannot register.
const (
	// KindReg is a registration link (GET/POST /register).
	KindReg = "reg"
	// KindPanel is a participant-panel magic link (GET/POST /panel).
	KindPanel = "panel"
)

// RegPayload is the decrypted content of a token: the Matrix handle of the
// sender, the unix time the token was issued, the token Kind, and MAC — a base64
// HMAC-SHA256 over (Handle, Issued, Kind) keyed by the shared TOKEN_SECRET.
type RegPayload struct {
	Handle string `json:"h"`
	Issued int64  `json:"t"`
	Kind   string `json:"k,omitempty"`
	MAC    string `json:"m,omitempty"`
}

// NormalizeKind maps an empty kind to KindReg. Tokens issued before the kind
// field existed carry no "k", so treating empty as "reg" keeps those links
// working; normalizing on BOTH the encode and decode side (before the MAC is
// computed) keeps the MAC of {} and {"k":"reg"} identical.
func NormalizeKind(kind string) string {
	if kind == "" {
		return KindReg
	}
	return kind
}

// tokenMAC returns the base64 HMAC-SHA256 of the canonical (handle, issued,
// kind) representation keyed by secret. The canonical form is
// "handle\nissued\nkind", so the fields cannot be ambiguously concatenated and
// the kind cannot be swapped without invalidating the MAC.
func tokenMAC(secret, handle string, issued int64, kind string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s\n%d\n%s", handle, issued, NormalizeKind(kind))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
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

// EncodeRegToken stamps p with an HMAC over (Handle, Issued, Kind) keyed by
// secret, marshals it to JSON and produces an age-encrypted, base64 token. An
// empty Kind is left empty on the wire but MACed as KindReg.
func EncodeRegToken(r age.Recipient, secret string, p RegPayload) (string, error) {
	p.MAC = tokenMAC(secret, p.Handle, p.Issued, p.Kind)
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return EncryptToken(r, string(b))
}

// DecodeRegToken reverses EncodeRegToken using the private identity, then
// verifies the HMAC over (Handle, Issued, Kind) keyed by secret in constant
// time. A missing or mismatching MAC (e.g. a forged or old-format token) is
// rejected. The returned Kind is normalized, so a pre-kind token reads as
// KindReg; callers compare it against the kind their endpoint expects.
func DecodeRegToken(id age.Identity, secret, token string) (RegPayload, error) {
	plain, err := DecryptToken(id, token)
	if err != nil {
		return RegPayload{}, err
	}
	var p RegPayload
	if err := json.Unmarshal([]byte(plain), &p); err != nil {
		return RegPayload{}, fmt.Errorf("unmarshal payload: %w", err)
	}
	want := tokenMAC(secret, p.Handle, p.Issued, p.Kind)
	if !hmac.Equal([]byte(want), []byte(p.MAC)) {
		return RegPayload{}, fmt.Errorf("invalid token authentication")
	}
	p.Kind = NormalizeKind(p.Kind)
	return p, nil
}

// RegisterLink appends the token to the base URL as a `t` query parameter.
func RegisterLink(base, token string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	// The token is standard base64 (may contain +, /, =); query-escape it.
	return base + sep + "t=" + url.QueryEscape(token)
}

// registerLink builds a link for "now" using the client's recipient/base,
// embedding the sender's Matrix handle so the web server can predefine the nick.
func (c *Client) registerLink(handle string) (string, error) {
	if c.Recipient == nil {
		return "", fmt.Errorf("no age recipient configured")
	}
	tok, err := EncodeRegToken(c.Recipient, c.TokenSecret, RegPayload{Handle: handle, Issued: time.Now().Unix(), Kind: KindReg})
	if err != nil {
		return "", err
	}
	return RegisterLink(c.LinkBase, tok), nil
}
