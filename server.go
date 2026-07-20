// HTTP wiring shared by every handler: the dependency bundle, the mux, the
// static pages, token decoding, the template render helpers and the small
// capacity/identity utilities. The per-area handlers live in register.go,
// panel.go, admin.go and api.go.

package main

import (
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/d33mobile/dday/internal/matrixbot"
	"github.com/d33mobile/dday/internal/store"

	"filippo.io/age"
)

// tokenTTL bounds how long a registration link stays valid after it was issued.
// A token older than this (or issued in the future beyond a small clock-skew
// tolerance) is rejected in decode().
const tokenTTL = 48 * time.Hour

// tokenFutureSkew tolerates a small amount of clock drift when the token's
// Issued time is ahead of the server's clock.
const tokenFutureSkew = 5 * time.Minute

// deps carries the runtime dependencies of the registration handlers, so the
// mux can be built in tests with an in-memory store, an ephemeral key and an
// injectable time gate.
type deps struct {
	store         *store.Store
	identity      age.Identity
	seatLimit     int // confirmed participant places (numbers 1..seatLimit)
	waitlistLimit int // waiting-list places (numbers seatLimit+1..seatLimit+waitlistLimit)
	isOpen        func() bool
	files         http.FileSystem // static files for GET /
	internalToken string          // bearer token guarding /api/registered; empty disables it
	adminToken    string          // bearer/query token guarding /admin; empty disables it
	tokenSecret   string          // shared HMAC key authenticating registration tokens
}

// total is the overall capacity: confirmed seats plus waiting-list places. A
// registration is refused only once total is reached.
func (d deps) total() int { return d.seatLimit + d.waitlistLimit }

// formView is the data model for the registration form template.
type formView struct {
	Title    string
	Token    string
	Nick     string
	City     string
	Email    string
	Error    string
	Count    int
	Limit    int
	Waitlist bool // true when confirmed seats are gone: this signup joins the waiting list
}

// resultView backs the success/duplicate/waitlist/message pages.
type resultView struct {
	Title       string
	Nick        string
	Number      int
	WaitlistPos int // position on the waiting list (number-seatLimit); 0 for confirmed participants
	Message     string
	Detail      string
}

// newMux builds the HTTP handler with every route, wrapped in the security
// middleware. It is the single place both main() and the tests construct.
func newMux(d deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("/register", d.handleRegister)
	mux.HandleFunc("/panel", d.handlePanel)
	mux.HandleFunc("/api/count", d.handleCount)
	mux.HandleFunc("/api/registered", d.handleRegistered)
	mux.HandleFunc("/api/registrations", d.handleRegistrations)
	mux.HandleFunc("/admin", d.handleAdmin)
	mux.HandleFunc("/privacy", d.handlePrivacy)
	mux.HandleFunc("/", d.handleRoot)

	return secure(mux)
}

// handleRoot serves the landing page for "/" only. Unlike http.FileServer it
// never walks the static directory, so STATIC_DIR=. (a dev convenience that
// points at the repo root) can never leak matrix.env, the age key or the SQLite
// DB via an arbitrary path — anything other than "/" is a 404.
func (d deps) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	d.serveStatic(w, "index.html")
}

// serveStatic writes one of the fixed, known HTML files from d.files. Only the
// names the handlers reference can be served — there is no path input, so a
// STATIC_DIR pointing at a directory with secrets cannot expose them.
func (d deps) serveStatic(w http.ResponseWriter, name string) {
	f, err := d.files.Open(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := io.Copy(w, f); err != nil {
		log.Printf("serve %s: %v", name, err)
	}
}

// methodNotAllowed writes a 405 with an Allow: GET header, for the GET-only
// read endpoints.
func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// ready reports whether registration can run (key loaded and DB open). When it
// is false the site still serves the landing page; only registration degrades.
func (d deps) ready() bool { return d.store != nil && d.identity != nil }

// waitlistPos maps a rank (position among current registrations, ordered by id)
// to a waiting-list position, or 0 for a confirmed participant. Status is
// derived from the rank rather than the participant number, so when someone
// withdraws everyone behind them moves up a place.
func (d deps) waitlistPos(rank int) int {
	if rank > d.seatLimit {
		return rank - d.seatLimit
	}
	return 0
}

func (d deps) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	d.serveStatic(w, "privacy.html")
}

// decode validates a token and checks it was issued for wantKind; on failure it
// writes a 400 page and returns ok=false. The kind is covered by the token's
// HMAC, so a registration link presented to /panel (or a panel link presented to
// /register) is rejected with a deliberately vague "Nieprawidłowy link".
func (d deps) decode(w http.ResponseWriter, token, wantKind string) (matrixbot.RegPayload, bool) {
	if strings.TrimSpace(token) == "" {
		d.renderMessage(w, http.StatusBadRequest, "Nieprawidłowy link",
			"Brak tokenu rejestracji.",
			"Skorzystaj z linku otrzymanego od bota na czacie Matrix.")
		return matrixbot.RegPayload{}, false
	}
	payload, err := matrixbot.DecodeRegToken(d.identity, d.tokenSecret, token)
	if err != nil {
		d.renderMessage(w, http.StatusBadRequest, "Nieprawidłowy link",
			"Ten link rejestracyjny jest nieprawidłowy lub uszkodzony.",
			"Poproś bota o nowy link na czacie Matrix.")
		return matrixbot.RegPayload{}, false
	}
	// TTL: reject a stale link, or one whose Issued time is too far in the
	// future (beyond a small clock-skew tolerance).
	elapsed := time.Now().Unix() - payload.Issued
	if elapsed > int64(tokenTTL/time.Second) || elapsed < -int64(tokenFutureSkew/time.Second) {
		d.renderMessage(w, http.StatusBadRequest, "Link wygasł",
			"Ten link rejestracyjny wygasł.",
			"Poproś bota o nowy link na czacie Matrix.")
		return matrixbot.RegPayload{}, false
	}
	// Kind scoping: the token must have been minted for this endpoint. The
	// message stays generic so it does not reveal which link the visitor holds.
	if matrixbot.NormalizeKind(payload.Kind) != matrixbot.NormalizeKind(wantKind) {
		d.renderMessage(w, http.StatusBadRequest, "Nieprawidłowy link",
			"Ten link jest nieprawidłowy.",
			"Poproś bota o nowy link na czacie Matrix.")
		return matrixbot.RegPayload{}, false
	}
	return payload, true
}

func (d deps) renderForm(w http.ResponseWriter, v formView) {
	v.Title = "Zapis"
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "form", v); err != nil {
		log.Printf("render form: %v", err)
	}
}

func (d deps) renderResult(w http.ResponseWriter, name string, v resultView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, v); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (d deps) renderMessage(w http.ResponseWriter, status int, title, msg, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := tmpl.ExecuteTemplate(w, "message", resultView{Title: title, Message: msg, Detail: detail}); err != nil {
		log.Printf("render message: %v", err)
	}
}

func (d deps) serverError(w http.ResponseWriter, ctx string, err error) {
	log.Printf("%s: %v", ctx, err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// nickFromHandle turns a Matrix MXID "@alice:hs.org" into the localpart "alice".
// Any string that does not match the @local:server shape is returned unchanged.
func nickFromHandle(handle string) string {
	if !strings.HasPrefix(handle, "@") {
		return handle
	}
	rest := handle[1:]
	i := strings.IndexByte(rest, ':')
	if i <= 0 {
		return handle
	}
	return rest[:i]
}
