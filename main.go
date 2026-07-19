// Command dday serves the D-Day landing page and the event registration flow.
//
// It embeds index.html and privacy.html into the binary, so the container is
// fully self-contained. Set STATIC_DIR to serve static files from the
// filesystem instead (useful for live-editing during development).
//
// Registration state is persisted in a pure-Go SQLite database (CGO off).
package main

import (
	"context"
	"embed"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/d33mobile/dday/internal/matrixbot"
	"github.com/d33mobile/dday/internal/regwindow"
	"github.com/d33mobile/dday/internal/store"

	"filippo.io/age"
)

//go:embed index.html privacy.html
var embedded embed.FS

const seatLimit = 20

func main() {
	port := env("PORT", "3329")

	healthcheck := flag.Bool("healthcheck", false, "probe /healthz on localhost and exit 0/1")
	flag.Parse()
	if *healthcheck {
		runHealthcheck(port)
		return
	}

	var files http.FileSystem
	if dir := os.Getenv("STATIC_DIR"); dir != "" {
		log.Printf("serving static files from %s", dir)
		files = http.Dir(dir)
	} else {
		sub, err := fs.Sub(embedded, ".")
		if err != nil {
			log.Fatalf("embed: %v", err)
		}
		files = http.FS(sub)
	}

	// Registration needs the private key and the database. If either is
	// unavailable, don't take the whole site down — keep serving the landing
	// page (and /privacy, /healthz) and let only the registration endpoints
	// degrade to 503 until they're configured.
	identity, err := loadIdentity()
	if err != nil {
		log.Printf("WARNING: registration disabled — age key: %v", err)
		identity = nil
	}

	st, err := store.Open(env("DB_PATH", "./dday.db"))
	if err != nil {
		log.Printf("WARNING: registration disabled — store: %v", err)
		st = nil
	} else {
		defer st.Close()
	}

	handler := newMux(deps{
		store:         st,
		identity:      identity,
		seatLimit:     seatLimit,
		isOpen:        regwindow.Open,
		files:         files,
		internalToken: os.Getenv("INTERNAL_TOKEN"),
		tokenSecret:   os.Getenv("TOKEN_SECRET"),
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Graceful shutdown: on SIGINT/SIGTERM (container redeploy) stop accepting
	// new requests, drain in-flight ones, then return so the deferred st.Close()
	// runs and the SQLite WAL is checkpointed cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("dday listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	stop() // restore default signal handling; a second signal now aborts hard
	log.Printf("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// loadIdentity resolves the age identity used to decrypt registration tokens.
// AGE_KEY_DATA (base64-encoded private key) takes precedence over the AGE_KEY
// file path — passing the key by value avoids container file-permission issues
// (a mounted 0600 key is unreadable by the distroless nonroot user).
func loadIdentity() (age.Identity, error) {
	if b64 := strings.TrimSpace(os.Getenv("AGE_KEY_DATA")); b64 != "" {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("AGE_KEY_DATA base64: %w", err)
		}
		return matrixbot.ParseIdentity(data)
	}
	return matrixbot.LoadIdentity(env("AGE_KEY", "config/dday_ed25519"))
}

// secure adds baseline security headers to every response.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// runHealthcheck probes the local /healthz endpoint; used by the Docker
// healthcheck since the distroless image has no shell or wget.
func runHealthcheck(port string) {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		log.Printf("healthcheck: %v", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("healthcheck: status %d", resp.StatusCode)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
