// JSON endpoints: the public capacity counter and the two token-guarded feeds
// the bot polls.

package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/d33mobile/dday/internal/regwindow"
)

func (d deps) handleCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	count := 0
	if d.store != nil {
		var err error
		count, err = d.store.Count()
		if err != nil {
			d.serverError(w, "count", err)
			return
		}
	}
	confirmed := count
	if confirmed > d.seatLimit {
		confirmed = d.seatLimit
	}
	waitlistCount := count - d.seatLimit
	if waitlistCount < 0 {
		waitlistCount = 0
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	// count/limit are kept for backward compatibility; confirmed/waitlist*
	// expose the two-tier capacity so the landing page can render both bars.
	// The *At / *Text fields make regwindow the single source of the dates: the
	// landing page overwrites its hardcoded fallbacks with these on every load.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"count":          count,
		"limit":          d.seatLimit,
		"waitlist":       d.waitlistLimit,
		"confirmed":      confirmed,
		"waitlistCount":  waitlistCount,
		"full":           count >= d.total(),
		"open":           d.isOpen(),
		"openAt":         regwindow.OpenAt().Unix(),
		"eventStartAt":   regwindow.EventStart().Unix(),
		"eventEndAt":     regwindow.EventEnd().Unix(),
		"openText":       regwindow.OpenStartText(),
		"openHowto":      regwindow.OpenHowtoText(),
		"openShort":      regwindow.OpenShort(),
		"openShortTime":  regwindow.OpenShortTime(),
		"eventText":      regwindow.EventText(),
		"eventShort":     regwindow.EventShort(),
		"eventShortTime": regwindow.EventShortTime(),
		"eventBadge":     regwindow.EventBadge(),
	})
}

// handleRegistered answers the bot's internal "is this handle registered?"
// query. It is guarded by a shared bearer token: when internalToken is empty
// the endpoint is disabled (404) so the registration list can never leak; a
// missing or wrong token is 401; a missing handle is 400. On success it returns
// {"registered": bool, "number": int}.
func (d deps) handleRegistered(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if d.internalToken == "" {
		http.NotFound(w, r)
		return
	}
	want := "Bearer " + d.internalToken
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	handle := strings.TrimSpace(r.URL.Query().Get("h"))
	if handle == "" {
		http.Error(w, "missing handle", http.StatusBadRequest)
		return
	}
	if d.store == nil {
		http.Error(w, "registration unavailable", http.StatusServiceUnavailable)
		return
	}
	number, registered, err := d.store.Number(handle)
	if err != nil {
		d.serverError(w, "registered", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"registered": registered,
		"number":     number,
	})
}

// registrationItem is one entry of the internal GET /api/registrations feed the
// bot polls to announce new signups. It deliberately carries no personal data
// beyond the public Matrix handle and the self-chosen nick: e-mail and city
// must never reach a public room, so they are not part of this shape at all.
type registrationItem struct {
	ID          int    `json:"id"`
	Handle      string `json:"handle"`
	Nick        string `json:"nick"`
	Rank        int    `json:"rank"`
	Confirmed   bool   `json:"confirmed"`
	WaitlistPos int    `json:"waitlistPos"`
}

// handleRegistrations serves the announcement feed: every registration with
// id > since, so the bot can post about the ones it has not announced yet.
// Auth mirrors /api/registered — an empty internalToken disables the endpoint
// (404), a missing/wrong bearer is 401, and only GET is allowed. A missing or
// unparsable "since" means 0 (everything). The response body is
// {"registrations":[{id,handle,nick,rank,confirmed,waitlistPos}, ...]} with
// rank taken from the position in the id-ordered list, so a withdrawal ahead of
// a row promotes it exactly like /admin and /panel do.
func (d deps) handleRegistrations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if d.internalToken == "" {
		http.NotFound(w, r)
		return
	}
	want := "Bearer " + d.internalToken
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if d.store == nil {
		http.Error(w, "registration unavailable", http.StatusServiceUnavailable)
		return
	}
	// A malformed "since" is treated as 0 rather than 400: the caller is our own
	// bot, and announcing from the start is a safer failure than a hard error.
	since, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("since")))
	regs, err := d.store.List()
	if err != nil {
		d.serverError(w, "registrations", err)
		return
	}
	items := make([]registrationItem, 0, len(regs))
	for i, reg := range regs {
		if reg.ID <= since {
			continue
		}
		pos := d.waitlistPos(i + 1)
		items = append(items, registrationItem{ID: reg.ID, Handle: reg.Handle,
			Nick: reg.Nick, Rank: i + 1, Confirmed: pos == 0, WaitlistPos: pos})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"registrations": items})
}
