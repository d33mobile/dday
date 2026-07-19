// Command bot runs the D-Day Matrix bot.
//
// On "!register" it opens a private DM with the sender and sends a registration
// link whose token is an age-encrypted timestamp (see internal/matrixbot).
//
// Config (from matrix.env / environment):
//
//	MATRIX_HOMESERVER   e.g. https://matrix.org
//	MATRIX_USER         e.g. @ddaybot:matrix.org
//	MATRIX_PASSWORD     bot password
//	AGE_PUB_DATA        base64 of the SSH ed25519 public key (recipient); preferred
//	AGE_PUB             SSH ed25519 public key file (recipient); fallback
//	REGISTER_URL        base link the token is appended to
//
// Note: the bot reads plaintext rooms only (no E2E encryption), so keep the
// command room unencrypted.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
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
	c.IsOpen = regwindow.Open

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
