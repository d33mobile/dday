// Command dday serves the static D-Day landing page.
//
// By default it serves index.html embedded into the binary, so the container
// is fully self-contained. Set STATIC_DIR to serve from the filesystem instead
// (useful for live-editing during development).
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"
)

//go:embed index.html
var embedded embed.FS

func main() {
	port := env("PORT", "3329")

	healthcheck := flag.Bool("healthcheck", false, "probe /healthz on localhost and exit 0/1")
	flag.Parse()
	if *healthcheck {
		runHealthcheck(port)
		return
	}

	addr := ":" + port

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

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/", secure(http.FileServer(files)))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("dday listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
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
