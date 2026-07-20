package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

// genPubKey returns a fresh SSH ed25519 public key as an authorized_keys line,
// the shape production feeds to loadRecipient (AGE_PUB / AGE_PUB_DATA).
func genPubKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("new public key: %v", err)
	}
	return string(ssh.MarshalAuthorizedKey(sshPub))
}

// mockRegistered stands up a fake web /api/registered endpoint that demands the
// shared bearer token and answers from a fixed handle->number map.
func mockRegistered(t *testing.T, wantToken string, reg map[string]int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/registered", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		num, ok := reg[r.URL.Query().Get("h")]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"registered": ok, "number": num})
	})
	return httptest.NewServer(mux)
}

func TestRegisteredChecker(t *testing.T) {
	const token = "internal-secret"
	srv := mockRegistered(t, token, map[string]int{"@alice:hs.org": 7})
	defer srv.Close()

	check := registeredChecker(srv.Client(), srv.URL, token)

	num, reg, err := check("@alice:hs.org")
	if err != nil {
		t.Fatalf("check registered handle: %v", err)
	}
	if !reg || num != 7 {
		t.Fatalf("registered handle = (%d,%v); want (7,true)", num, reg)
	}

	num, reg, err = check("@bob:hs.org")
	if err != nil {
		t.Fatalf("check unregistered handle: %v", err)
	}
	if reg || num != 0 {
		t.Fatalf("unregistered handle = (%d,%v); want (0,false)", num, reg)
	}
}

// TestRegisteredCheckerWrongTokenErrors asserts the closure surfaces a non-200
// response (here a 401 from a rejected bearer token) as an error — the bot then
// treats it fail-open, but the closure's own contract is to return err.
func TestRegisteredCheckerWrongTokenErrors(t *testing.T) {
	srv := mockRegistered(t, "right-token", map[string]int{"@alice:hs.org": 1})
	defer srv.Close()

	check := registeredChecker(srv.Client(), srv.URL, "wrong-token")
	if _, _, err := check("@alice:hs.org"); err == nil {
		t.Fatal("expected an error when the bearer token is rejected (401)")
	}
}

// TestRegisteredCheckerSendsAuthAndHandle verifies the request the closure
// builds: the Authorization bearer header and the URL-escaped handle param that
// round-trips back to the raw handle server-side.
func TestRegisteredCheckerSendsAuthAndHandle(t *testing.T) {
	const token = "tok"
	var gotAuth, gotHandle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotHandle = r.URL.Query().Get("h")
		_, _ = w.Write([]byte(`{"registered":false,"number":0}`))
	}))
	defer srv.Close()

	check := registeredChecker(srv.Client(), srv.URL, token)
	const handle = "@user:hs with space"
	if _, _, err := check(handle); err != nil {
		t.Fatalf("check: %v", err)
	}
	if gotAuth != "Bearer "+token {
		t.Errorf("Authorization = %q; want %q", gotAuth, "Bearer "+token)
	}
	if gotHandle != handle {
		t.Errorf("handle param = %q; want %q", gotHandle, handle)
	}
}

func TestOriginOf(t *testing.T) {
	got, err := originOf("https://dday.hs-ldz.pl/register")
	if err != nil {
		t.Fatalf("originOf: %v", err)
	}
	if got != "https://dday.hs-ldz.pl" {
		t.Errorf("origin = %q; want https://dday.hs-ldz.pl", got)
	}
	if _, err := originOf("not-a-url"); err == nil {
		t.Error("expected an error for a URL lacking scheme/host")
	}
}

// TestPanelURL covers the three ways the participant-panel base is resolved:
// an explicit PANEL_URL wins, otherwise it is derived from REGISTER_URL's
// origin, and a REGISTER_URL without a scheme/host disables panel links ("").
func TestPanelURL(t *testing.T) {
	t.Setenv("PANEL_URL", "")
	if got := panelURL("https://dday.hs-ldz.pl/register"); got != "https://dday.hs-ldz.pl/panel" {
		t.Errorf("panelURL = %q; want the derived https://dday.hs-ldz.pl/panel", got)
	}
	if got := panelURL("not-a-url"); got != "" {
		t.Errorf("panelURL = %q; want \"\" when REGISTER_URL has no origin", got)
	}

	t.Setenv("PANEL_URL", "  https://other.example/p  ")
	if got := panelURL("https://dday.hs-ldz.pl/register"); got != "https://other.example/p" {
		t.Errorf("panelURL = %q; want the explicit (trimmed) PANEL_URL", got)
	}
}

func TestLoadRecipientFromEnvData(t *testing.T) {
	pubLine := genPubKey(t)
	t.Setenv("AGE_PUB_DATA", base64.StdEncoding.EncodeToString([]byte(pubLine)))
	t.Setenv("AGE_PUB", "")

	r, err := loadRecipient()
	if err != nil {
		t.Fatalf("loadRecipient: %v", err)
	}
	if r == nil {
		t.Fatal("nil recipient")
	}
}

func TestLoadRecipientFromFile(t *testing.T) {
	pubLine := genPubKey(t)
	path := filepath.Join(t.TempDir(), "key.pub")
	if err := os.WriteFile(path, []byte(pubLine), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	t.Setenv("AGE_PUB_DATA", "")
	t.Setenv("AGE_PUB", path)

	r, err := loadRecipient()
	if err != nil {
		t.Fatalf("loadRecipient: %v", err)
	}
	if r == nil {
		t.Fatal("nil recipient")
	}
}

func TestLoadRecipientBadBase64(t *testing.T) {
	t.Setenv("AGE_PUB_DATA", "!!! not base64 !!!")
	if _, err := loadRecipient(); err == nil {
		t.Fatal("expected an error for invalid AGE_PUB_DATA base64")
	}
}

func TestEnv(t *testing.T) {
	t.Setenv("DDAY_TEST_ENV", "")
	if got := env("DDAY_TEST_ENV", "fallback"); got != "fallback" {
		t.Errorf("env unset = %q; want fallback", got)
	}
	t.Setenv("DDAY_TEST_ENV", "set")
	if got := env("DDAY_TEST_ENV", "fallback"); got != "set" {
		t.Errorf("env set = %q; want set", got)
	}
}

func TestMustEnv(t *testing.T) {
	t.Setenv("DDAY_MUST_ENV", "value")
	if got := mustEnv("DDAY_MUST_ENV"); got != "value" {
		t.Errorf("mustEnv = %q; want value", got)
	}
}
