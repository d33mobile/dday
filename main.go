// Command dday serves the D-Day landing page and the event registration flow.
//
// It embeds index.html and privacy.html into the binary, so the container is
// fully self-contained. Set STATIC_DIR to serve static files from the
// filesystem instead (useful for live-editing during development).
//
// Registration state is persisted in a pure-Go SQLite database (CGO off).
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"
	_ "time/tzdata" // embed the tz database so Europe/Warsaw resolves on distroless

	"github.com/d33mobile/dday/internal/matrixbot"
	"github.com/d33mobile/dday/internal/store"
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

	identity, err := matrixbot.LoadIdentity(env("AGE_KEY", "config/dday_ed25519"))
	if err != nil {
		log.Fatalf("load age identity: %v", err)
	}

	st, err := store.Open(env("DB_PATH", "./dday.db"))
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	handler := newMux(deps{
		store:     st,
		identity:  identity,
		seatLimit: seatLimit,
		isOpen:    registrationOpen,
		files:     files,
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("dday listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// openMoment is the instant registration opens: 2026-07-26 15:00 Europe/Warsaw.
var openMoment = time.Date(2026, 7, 26, 15, 0, 0, 0, warsaw())

// registrationOpen is the default time gate. REGISTRATION_OPEN=1/true forces it
// open; otherwise it opens once we pass openMoment in the Warsaw timezone.
func registrationOpen() bool {
	switch os.Getenv("REGISTRATION_OPEN") {
	case "1", "true", "TRUE", "yes":
		return true
	}
	return !time.Now().Before(openMoment)
}

// warsaw returns the Europe/Warsaw location, falling back to a fixed +02:00
// (CEST) zone if the tz database is unavailable. time/tzdata is imported so the
// lookup succeeds even on distroless images without system tzdata.
func warsaw() *time.Location {
	loc, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		return time.FixedZone("CEST", 2*60*60)
	}
	return loc
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
