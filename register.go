// Registration flow: the form behind a bot-issued registration link (GET) and
// the submission that creates the participant row (POST).

package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"

	"github.com/d33mobile/dday/internal/matrixbot"
	"github.com/d33mobile/dday/internal/regwindow"
	"github.com/d33mobile/dday/internal/store"
)

// Server-side input bounds. A submission exceeding either is re-rendered with an
// error instead of being stored — a cheap guard against oversized bot payloads.
const (
	maxCityLen  = 120 // bytes, after TrimSpace
	maxEmailLen = 254 // bytes, the practical SMTP address maximum
)

// handleRegister dispatches GET (render form) and POST (process submission).
func (d deps) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !d.ready() {
		d.renderMessage(w, http.StatusServiceUnavailable, "Rejestracja chwilowo niedostępna",
			"Zapisy są tymczasowo niedostępne.",
			"Spróbuj ponownie za chwilę lub napisz na czacie Matrix.")
		return
	}
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
	payload, ok := d.decode(w, token, matrixbot.KindReg)
	if !ok {
		return
	}
	nick := nickFromHandle(payload.Handle)

	// Link expiry: once this handle is registered the link is spent. Show the
	// confirmation instead of the form, so a re-used link cannot open a second
	// registration attempt (the POST path already dedupes on ErrDuplicate).
	number, registered, err := d.store.Number(payload.Handle)
	if err != nil {
		d.serverError(w, "number", err)
		return
	}
	if registered {
		d.renderMessage(w, http.StatusOK, "Link już wykorzystany",
			fmt.Sprintf("Jesteś już zapisany (#%d).", number),
			"Twoja rejestracja jest kompletna — ten link został już wykorzystany.")
		return
	}

	if !d.isOpen() {
		d.renderMessage(w, http.StatusOK, "Zapisy jeszcze nieotwarte",
			"Zapisy na D-Day nie są jeszcze otwarte.",
			"Start zapisów: "+regwindow.OpenStartText()+". Wróć tutaj przez ten sam link.")
		return
	}

	count, err := d.store.Count()
	if err != nil {
		d.serverError(w, "count", err)
		return
	}
	if count >= d.total() {
		d.renderMessage(w, http.StatusOK, "Brak miejsc",
			"Niestety, brak wolnych miejsc.",
			"Lista uczestników i lista rezerwowa zostały wyczerpane.")
		return
	}

	// Confirmed seats gone but waiting-list places remain: warn that this signup
	// joins the waiting list, not the confirmed roster.
	d.renderForm(w, formView{Nick: nick, Token: token, Count: count,
		Limit: d.total(), Waitlist: count >= d.seatLimit})
}

func (d deps) registerPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	token := r.PostFormValue("t")
	// The token is the source of truth for identity, never the form fields.
	payload, ok := d.decode(w, token, matrixbot.KindReg)
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
			"Start zapisów: "+regwindow.OpenStartText()+".")
		return
	}

	count, err := d.store.Count()
	if err != nil {
		d.serverError(w, "count", err)
		return
	}

	// Validate user input before touching the store; re-render the form on error.
	reject := func(msg string) {
		d.renderForm(w, formView{Nick: nick, Token: token, City: city, Email: email,
			Count: count, Limit: d.total(), Waitlist: count >= d.seatLimit, Error: msg})
	}
	switch {
	case city == "":
		reject("Podaj miejscowość.")
		return
	case len(city) > maxCityLen:
		reject("Miejscowość jest za długa.")
		return
	case len(email) > maxEmailLen:
		reject("Adres e-mail jest za długi.")
		return
	}
	addr, err := mail.ParseAddress(email)
	if err != nil {
		reject("Podaj poprawny adres e-mail.")
		return
	}
	// Store the bare address, not the raw "Name <addr>" form the parser accepts.
	email = addr.Address

	number, err := d.store.Register(handle, nick, city, email, d.total())
	switch {
	case errors.Is(err, store.ErrDuplicate):
		// Neutral wording — an existing registration may be confirmed or on the
		// waiting list; either way the link is spent. The status comes from the
		// current rank, so a withdrawal ahead of this handle is reflected.
		rank, _, rerr := d.store.Rank(handle)
		if rerr != nil {
			d.serverError(w, "rank", rerr)
			return
		}
		d.renderResult(w, "duplicate", resultView{Title: "Już zapisany", Nick: nick,
			Number: number, WaitlistPos: d.waitlistPos(rank)})
	case errors.Is(err, store.ErrFull):
		d.renderMessage(w, http.StatusOK, "Brak miejsc",
			"Niestety, brak wolnych miejsc.",
			"Lista uczestników i lista rezerwowa zostały wyczerpane.")
	case err != nil:
		d.serverError(w, "register", err)
	default:
		// The new row is the last one, so its rank is the current row count.
		rank, cerr := d.store.Count()
		if cerr != nil {
			d.serverError(w, "count", cerr)
			return
		}
		if pos := d.waitlistPos(rank); pos > 0 {
			// Confirmed seats were full: this registration joined the waiting list.
			d.renderResult(w, "waitlist", resultView{Title: "Lista rezerwowa", Nick: nick,
				Number: number, WaitlistPos: pos})
			return
		}
		d.renderResult(w, "success", resultView{Title: "Zapisano", Nick: nick, Number: number})
	}
}
