// Command bot runs the D-Day Matrix bot.
//
// On "!start" (legacy alias: "!register") it opens a private DM with the sender
// and sends a link whose token is an age-encrypted timestamp (see
// internal/matrixbot): a registration link for a newcomer, or a magic link to
// the participant panel for someone who is already signed up.
//
// Config (from matrix.env / environment):
//
//	MATRIX_HOMESERVER   e.g. https://matrix.org
//	MATRIX_USER         e.g. @ddaybot:matrix.org
//	MATRIX_PASSWORD     bot password
//	AGE_PUB_DATA        base64 of the SSH ed25519 public key (recipient); preferred
//	AGE_PUB             SSH ed25519 public key file (recipient); fallback
//	REGISTER_URL        base link the token is appended to
//	PANEL_URL           base link of the participant panel; defaults to the
//	                    origin of REGISTER_URL plus "/panel"
//	TOKEN_SECRET        shared HMAC key authenticating the link token (must match web)
//	ALLOWED_ROOMS       optional comma-separated room ids; if set, !start is
//	                    only honoured in those rooms (unset = every room)
//
// Note: the bot reads plaintext rooms only (no E2E encryption), so keep the
// command room unencrypted.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"filippo.io/age"

	"github.com/d33mobile/dday/internal/matrixbot"
	"github.com/d33mobile/dday/internal/regwindow"
)

func main() {
	hs := mustEnv("MATRIX_HOMESERVER")
	user := mustEnv("MATRIX_USER")
	pass := mustEnv("MATRIX_PASSWORD")
	registerURL := env("REGISTER_URL", "https://dday.hs-ldz.pl/")

	recipient, err := loadRecipient()
	if err != nil {
		log.Fatalf("load recipient: %v", err)
	}

	c := matrixbot.New(hs)
	c.Recipient = recipient
	c.LinkBase = registerURL
	c.TokenSecret = os.Getenv("TOKEN_SECRET")
	c.IsOpen = regwindow.Open

	// Participant-panel base URL. PANEL_URL wins; otherwise it is derived from
	// REGISTER_URL's origin, which is where the web service serves /panel. A
	// REGISTER_URL without scheme/host leaves PanelBase empty, and the bot then
	// falls back to the link-less "already signed up" reply instead of handing
	// out a broken URL.
	c.PanelBase = panelURL(registerURL)
	if c.PanelBase == "" {
		log.Printf("no PANEL_URL and REGISTER_URL has no usable origin; panel magic links disabled")
	} else {
		log.Printf("participant panel links point at %s", c.PanelBase)
	}

	// Optional allowlist: when ALLOWED_ROOMS is set (comma-separated room ids),
	// the bot reacts to "!start" only in those rooms. Unset means everywhere.
	if rooms := strings.TrimSpace(os.Getenv("ALLOWED_ROOMS")); rooms != "" {
		c.AllowedRooms = matrixbot.ParseAllowedRooms(rooms)
		log.Printf("restricting !start to %d allowed room(s)", len(c.AllowedRooms))
	}

	// When INTERNAL_TOKEN is set, ask the web service whether a handle is
	// already registered before issuing a link. Without it the check is left
	// nil (fail-open): the bot always issues a link and the web POST dedupes.
	if tok := strings.TrimSpace(os.Getenv("INTERNAL_TOKEN")); tok != "" {
		origin, err := originOf(registerURL)
		if err != nil {
			log.Fatalf("REGISTER_URL: %v", err)
		}
		c.CheckRegistered = registeredChecker(c.HTTP, origin, tok)
		log.Printf("already-registered check enabled against %s", origin)
	}

	// Optional on-disk persistence of the DM room cache (handle -> room id). When
	// DM_CACHE_PATH points at a file on a volume, the cache survives restarts, so
	// after a redeploy the bot answers a known user in the same DM without asking
	// the server (m.direct / createRoom). Unset keeps the cache purely in-memory.
	c.CachePath = env("DM_CACHE_PATH", "")
	if c.CachePath != "" {
		n, err := c.LoadCache()
		if err != nil {
			log.Printf("load DM cache from %s: %v (continuing empty)", c.CachePath, err)
		} else {
			log.Printf("loaded %d DM cache entr(y/ies) from %s", n, c.CachePath)
		}
	}

	if err := c.Login(user, pass); err != nil {
		log.Fatalf("login: %v", err)
	}
	log.Printf("logged in as %s", c.Self)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := c.Run(ctx); err != nil {
		log.Fatal(err)
	}
}

// loadRecipient resolves the age recipient (SSH ed25519 public key) used to
// encrypt registration link tokens. AGE_PUB_DATA (base64 of the public key)
// takes precedence over the AGE_PUB file path — passing the key by value avoids
// container config-mount issues (matrix.env sets AGE_PUB to a host path that
// does not exist inside the image).
func loadRecipient() (age.Recipient, error) {
	if b64 := strings.TrimSpace(os.Getenv("AGE_PUB_DATA")); b64 != "" {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("AGE_PUB_DATA base64: %w", err)
		}
		return matrixbot.ParseRecipient(string(data))
	}
	return matrixbot.LoadRecipient(mustEnv("AGE_PUB"))
}

// panelURL resolves the base URL of the participant panel: the explicit
// PANEL_URL env var, else the origin of registerURL with "/panel" appended. It
// returns "" when neither is available (registerURL lacks a scheme/host), which
// the caller treats as "panel magic links disabled".
func panelURL(registerURL string) string {
	if v := strings.TrimSpace(os.Getenv("PANEL_URL")); v != "" {
		return v
	}
	origin, err := originOf(registerURL)
	if err != nil {
		return ""
	}
	return origin + "/panel"
}

// originOf extracts the scheme://host origin from a full URL.
func originOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("URL %q lacks scheme/host", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

// registeredChecker returns a CheckRegistered function that queries the web
// service's internal GET /api/registered?h= endpoint with the shared bearer
// token. A non-200 response or transport error surfaces as an error, which the
// bot treats fail-open.
func registeredChecker(httpc *http.Client, origin, token string) func(string) (int, bool, error) {
	return func(handle string) (int, bool, error) {
		req, err := http.NewRequest(http.MethodGet, origin+"/api/registered?h="+url.QueryEscape(handle), nil)
		if err != nil {
			return 0, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := httpc.Do(req)
		if err != nil {
			return 0, false, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return 0, false, fmt.Errorf("GET /api/registered -> %d", resp.StatusCode)
		}
		var out struct {
			Registered bool `json:"registered"`
			Number     int  `json:"number"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return 0, false, err
		}
		return out.Number, out.Registered, nil
	}
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("missing required env %s", k)
	}
	return v
}
