package matrixbot

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTokenRoundTrip(t *testing.T) {
	r, id := genKeypair(t)

	now := strconv.FormatInt(time.Now().Unix(), 10)
	token, err := EncryptToken(r, now)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}

	got, err := DecryptToken(id, token)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != now {
		t.Fatalf("round-trip mismatch: got %q want %q", got, now)
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	r, _ := genKeypair(t)
	_, otherID := genKeypair(t)

	token, err := EncryptToken(r, "1234567890")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := DecryptToken(otherID, token); err == nil {
		t.Fatal("expected decryption with the wrong key to fail")
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
