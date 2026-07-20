// Read-only admin overview of every registration, and the token check guarding
// it.

package main

import (
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/d33mobile/dday/internal/regwindow"
)

// adminRow is one line of the admin table: the immutable participant number
// plus the standing derived from the row's rank, and the stored personal data.
type adminRow struct {
	Number    int
	Status    string // "uczestnik" or "rezerwa #N"
	Confirmed bool
	Nick      string
	Handle    string
	City      string
	Email     string
	Created   string // formatted in Europe/Warsaw
}

// adminView backs the read-only admin page: capacity summary plus every row.
type adminView struct {
	Title         string
	Confirmed     int
	SeatLimit     int
	Waitlist      int
	WaitlistLimit int
	Total         int
	Capacity      int
	Rows          []adminRow
}

// handleAdmin renders the read-only admin overview of every registration. Auth
// mirrors /api/registered: an empty adminToken disables the endpoint entirely
// (404, so its existence is not advertised), and the token is compared in
// constant time. Because an operator opens this in a browser, the token is also
// accepted as the "t" query parameter — Referrer-Policy: no-referrer is set
// globally, so a token in the URL is not leaked onward, and Cache-Control:
// no-store keeps the personal data out of caches.
func (d deps) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if d.adminToken == "" {
		http.NotFound(w, r)
		return
	}
	if !d.adminAuthorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if d.store == nil {
		d.renderMessage(w, http.StatusServiceUnavailable, "Panel admina niedostępny",
			"Baza rejestracji jest niedostępna.", "Spróbuj ponownie za chwilę.")
		return
	}
	regs, err := d.store.List()
	if err != nil {
		d.serverError(w, "list", err)
		return
	}

	v := adminView{Title: "Panel admina", SeatLimit: d.seatLimit,
		WaitlistLimit: d.waitlistLimit, Total: len(regs), Capacity: d.total()}
	for i, reg := range regs {
		// Rank is the position in the id-ordered list, so a withdrawal ahead of
		// this row promotes it — same rule as /panel and /register.
		row := adminRow{Number: reg.ID, Nick: reg.Nick, Handle: reg.Handle,
			City: reg.City, Email: reg.Email,
			Created: time.Unix(reg.CreatedAt, 0).In(regwindow.Warsaw()).Format("2006-01-02 15:04")}
		if pos := d.waitlistPos(i + 1); pos > 0 {
			row.Status = fmt.Sprintf("rezerwa #%d", pos)
			v.Waitlist++
		} else {
			row.Status = "uczestnik"
			row.Confirmed = true
			v.Confirmed++
		}
		v.Rows = append(v.Rows, row)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "admin", v); err != nil {
		log.Printf("render admin: %v", err)
	}
}

// adminAuthorized reports whether the request carries the admin token, either as
// an Authorization: Bearer header or as the "t" query parameter. Both are
// compared in constant time; the query form is evaluated only when the header is
// absent, so a wrong header cannot be bypassed silently.
func (d deps) adminAuthorized(r *http.Request) bool {
	if h := r.Header.Get("Authorization"); h != "" {
		return subtle.ConstantTimeCompare([]byte(h), []byte("Bearer "+d.adminToken)) == 1
	}
	return subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("t")), []byte(d.adminToken)) == 1
}
