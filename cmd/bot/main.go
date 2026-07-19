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
//	AGE_PUB             SSH ed25519 public key file (recipient)
//	REGISTER_URL        base link the token is appended to
//
// Note: the bot reads plaintext rooms only (no E2E encryption), so keep the
// command room unencrypted.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/d33mobile/dday/internal/matrixbot"
)

func main() {
	hs := mustEnv("MATRIX_HOMESERVER")
	user := mustEnv("MATRIX_USER")
	pass := mustEnv("MATRIX_PASSWORD")
	pubPath := mustEnv("AGE_PUB")
	registerURL := env("REGISTER_URL", "https://dday.hs-ldz.pl/")

	recipient, err := matrixbot.LoadRecipient(pubPath)
	if err != nil {
		log.Fatalf("load recipient: %v", err)
	}

	c := matrixbot.New(hs)
	c.Recipient = recipient
	c.LinkBase = registerURL

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
