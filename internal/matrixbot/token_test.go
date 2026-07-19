package matrixbot

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

const testSecret = "test-token-secret"

func TestRegTokenRoundTrip(t *testing.T) {
	r, id := genKeypair(t)

	want := RegPayload{Handle: "@alice:mock", Issued: time.Now().Unix()}
	token, err := EncodeRegToken(r, testSecret, want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}

	got, err := DecodeRegToken(id, testSecret, token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Handle != want.Handle {
		t.Fatalf("handle mismatch: got %q want %q", got.Handle, want.Handle)
	}
	if got.Issued != want.Issued {
		t.Fatalf("issued mismatch: got %d want %d", got.Issued, want.Issued)
	}
}

func TestDecodeWrongKeyFails(t *testing.T) {
	r, _ := genKeypair(t)
	_, otherID := genKeypair(t)

	token, err := EncodeRegToken(r, testSecret, RegPayload{Handle: "@bob:mock", Issued: 1234567890})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := DecodeRegToken(otherID, testSecret, token); err == nil {
		t.Fatal("expected decryption with the wrong key to fail")
	}
}

// TestDecodeWrongSecretFails asserts the HMAC layer: a token authenticated with
// one secret must not verify under another, even though the recipient key (which
// is all an attacker gets from the .pub) is correct.
func TestDecodeWrongSecretFails(t *testing.T) {
	r, id := genKeypair(t)

	token, err := EncodeRegToken(r, "right-secret", RegPayload{Handle: "@bob:mock", Issued: 1234567890})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := DecodeRegToken(id, "wrong-secret", token); err == nil {
		t.Fatal("expected a token with a mismatching HMAC to be rejected")
	}
}

// TestDecodeMissingMACFails asserts that an old-format token (no "m" field),
// which anyone holding the public key could forge, is rejected.
func TestDecodeMissingMACFails(t *testing.T) {
	r, id := genKeypair(t)

	// Encrypt a legacy payload directly, bypassing the HMAC stamping.
	token, err := EncryptToken(r, `{"h":"@bob:mock","t":1234567890}`)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := DecodeRegToken(id, testSecret, token); err == nil {
		t.Fatal("expected a token without a MAC to be rejected")
	}
}

func TestRegisterLink(t *testing.T) {
	got := RegisterLink("https://dday.hs-ldz.pl/register", "a+b/c=")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Query().Get("t") != "a+b/c=" {
		t.Fatalf("token param not preserved: %q", u.Query().Get("t"))
	}

	// Base that already has a query should get an & separator.
	got2 := RegisterLink("https://x/r?a=1", "tok")
	if !strings.Contains(got2, "?a=1&t=") {
		t.Fatalf("expected & separator, got %q", got2)
	}
}
