package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/mail"
	"strings"

	"github.com/d33mobile/dday/internal/matrixbot"
	"github.com/d33mobile/dday/internal/store"

	"filippo.io/age"
)

// openStart is the human-readable moment registration opens, shown on the
// "closed" page. Matches the countdown target in index.html.
const openStart = "niedziela 26 lipca 2026, 15:00 (czasu polskiego)"

// deps carries the runtime dependencies of the registration handlers, so the
// mux can be built in tests with an in-memory store, an ephemeral key and an
// injectable time gate.
type deps struct {
	store     *store.Store
	identity  age.Identity
	seatLimit int
	isOpen    func() bool
	files     http.FileSystem // static files for GET /
}

// formView is the data model for the registration form template.
type formView struct {
	Title string
	Token string
	Nick  string
	City  string
	Email string
	Error string
	Count int
	Limit int
}

// resultView backs the success/duplicate/message pages.
type resultView struct {
	Title   string
	Nick    string
	Number  int
	Message string
	Detail  string
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
	mux.HandleFunc("/api/count", d.handleCount)
	mux.HandleFunc("/privacy", d.handlePrivacy)
	mux.Handle("/", http.FileServer(d.files))

	return secure(mux)
}

// handleRegister dispatches GET (render form) and POST (process submission).
func (d deps) handleRegister(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		d.registerGet(w, r)
	case http.MethodPost:
		d.registerPost(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (d deps) registerGet(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	payload, ok := d.decode(w, token)
	if !ok {
		return
	}
	nick := nickFromHandle(payload.Handle)

	if !d.isOpen() {
		d.renderMessage(w, http.StatusOK, "Zapisy jeszcze nieotwarte",
			"Zapisy na D-Day nie są jeszcze otwarte.",
			"Start zapisów: "+openStart+". Wróć tutaj przez ten sam link.")
		return
	}

	count, err := d.store.Count()
	if err != nil {
		d.serverError(w, "count", err)
		return
	}
	if count >= d.seatLimit {
		d.renderMessage(w, http.StatusOK, "Brak miejsc",
			"Niestety, brak wolnych miejsc.",
			"Limit uczestników został wyczerpany.")
		return
	}

	d.renderForm(w, formView{Nick: nick, Token: token, Count: count, Limit: d.seatLimit})
}

func (d deps) registerPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	token := r.PostFormValue("t")
	// The token is the source of truth for identity, never the form fields.
	payload, ok := d.decode(w, token)
	if !ok {
		return
	}
	handle := payload.Handle
	nick := nickFromHandle(handle)
	city := strings.TrimSpace(r.PostFormValue("city"))
	email := strings.TrimSpace(r.PostFormValue("email"))

	if !d.isOpen() {
		d.renderMessage(w, http.StatusOK, "Zapisy jeszcze nieotwarte",
			"Zapisy na D-Day nie są jeszcze otwarte.",
			"Start zapisów: "+openStart+".")
		return
	}

	count, err := d.store.Count()
	if err != nil {
		d.serverError(w, "count", err)
		return
	}

	// Validate user input before touching the store; re-render the form on error.
	if city == "" {
		d.renderForm(w, formView{Nick: nick, Token: token, City: city, Email: email,
			Count: count, Limit: d.seatLimit, Error: "Podaj miejscowość."})
		return
	}
	if _, err := mail.ParseAddress(email); err != nil {
		d.renderForm(w, formView{Nick: nick, Token: token, City: city, Email: email,
			Count: count, Limit: d.seatLimit, Error: "Podaj poprawny adres e-mail."})
		return
	}

	number, err := d.store.Register(handle, nick, city, email, d.seatLimit)
	switch {
	case errors.Is(err, store.ErrDuplicate):
		d.renderResult(w, "duplicate", resultView{Title: "Już zapisany", Nick: nick, Number: number})
	case errors.Is(err, store.ErrFull):
		d.renderMessage(w, http.StatusOK, "Brak miejsc",
			"Niestety, brak wolnych miejsc.",
			"Limit uczestników został wyczerpany.")
	case err != nil:
		d.serverError(w, "register", err)
	default:
		d.renderResult(w, "success", resultView{Title: "Zapisano", Nick: nick, Number: number})
	}
}

func (d deps) handleCount(w http.ResponseWriter, _ *http.Request) {
	count, err := d.store.Count()
	if err != nil {
		d.serverError(w, "count", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"count": count,
		"limit": d.seatLimit,
		"open":  d.isOpen(),
	})
}

func (d deps) handlePrivacy(w http.ResponseWriter, _ *http.Request) {
	f, err := d.files.Open("privacy.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := io.Copy(w, f); err != nil {
		log.Printf("privacy: %v", err)
	}
}

// decode validates a token; on failure it writes a 400 page and returns ok=false.
func (d deps) decode(w http.ResponseWriter, token string) (matrixbot.RegPayload, bool) {
	if strings.TrimSpace(token) == "" {
		d.renderMessage(w, http.StatusBadRequest, "Nieprawidłowy link",
			"Brak tokenu rejestracji.",
			"Skorzystaj z linku otrzymanego od bota na czacie Matrix.")
		return matrixbot.RegPayload{}, false
	}
	payload, err := matrixbot.DecodeRegToken(d.identity, token)
	if err != nil {
		d.renderMessage(w, http.StatusBadRequest, "Nieprawidłowy link",
			"Ten link rejestracyjny jest nieprawidłowy lub uszkodzony.",
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
