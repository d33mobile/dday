// Participant panel: the magic-link page showing a participant's standing and
// the single action it offers — withdrawing from the event.

package main

import (
	"log"
	"net/http"

	"github.com/d33mobile/dday/internal/matrixbot"
)

// panelView backs the participant panel page: identity, current standing and
// the token that authorizes the single action (withdrawal).
type panelView struct {
	Title       string
	Nick        string
	Token       string
	Number      int
	WaitlistPos int // waiting-list position; 0 for a confirmed participant
}

// handlePanel dispatches the participant panel: GET renders it, POST performs
// the single available action (withdrawing from the event).
func (d deps) handlePanel(w http.ResponseWriter, r *http.Request) {
	if !d.ready() {
		d.renderMessage(w, http.StatusServiceUnavailable, "Panel chwilowo niedostępny",
			"Panel uczestnika jest tymczasowo niedostępny.",
			"Spróbuj ponownie za chwilę lub napisz na czacie Matrix.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		d.panelGet(w, r)
	case http.MethodPost:
		d.panelPost(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// notRegistered is the neutral page shown when the panel token is valid but the
// handle has no registration (never signed up, or already withdrawn).
func (d deps) notRegistered(w http.ResponseWriter) {
	d.renderMessage(w, http.StatusOK, "Nie jesteś zapisany",
		"Nie jesteś zapisany na D-Day.",
		"Napisz `!start` do bota na Matrixie, żeby się zapisać.")
}

func (d deps) panelGet(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	payload, ok := d.decode(w, token, matrixbot.KindPanel)
	if !ok {
		return
	}
	v, ok := d.panelView(w, payload.Handle, token)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "panel", v); err != nil {
		log.Printf("render panel: %v", err)
	}
}

// panelView assembles the panel data for handle, or renders the "not
// registered" page and reports ok=false. The participant number stays the
// immutable row id; the confirmed/waiting-list status comes from the rank.
func (d deps) panelView(w http.ResponseWriter, handle, token string) (panelView, bool) {
	number, registered, err := d.store.Number(handle)
	if err != nil {
		d.serverError(w, "number", err)
		return panelView{}, false
	}
	if !registered {
		d.notRegistered(w)
		return panelView{}, false
	}
	rank, _, err := d.store.Rank(handle)
	if err != nil {
		d.serverError(w, "rank", err)
		return panelView{}, false
	}
	return panelView{Title: "Twój panel", Nick: nickFromHandle(handle), Token: token,
		Number: number, WaitlistPos: d.waitlistPos(rank)}, true
}

func (d deps) panelPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// The token is the source of truth for identity, never the form fields.
	payload, ok := d.decode(w, r.PostFormValue("t"), matrixbot.KindPanel)
	if !ok {
		return
	}
	deleted, err := d.store.Delete(payload.Handle)
	if err != nil {
		d.serverError(w, "delete", err)
		return
	}
	if !deleted {
		d.notRegistered(w)
		return
	}
	d.renderResult(w, "panel_done", resultView{Title: "Udział wycofany",
		Nick: nickFromHandle(payload.Handle)})
}
