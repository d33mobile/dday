package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/d33mobile/dday/internal/regwindow"
)

// TestApiCountDateFields asserts /api/count publishes the configured dates and
// their Polish renderings, so index.html never has to hardcode them: setting
// the regwindow env vars must change every field.
func TestApiCountDateFields(t *testing.T) {
	t.Setenv(regwindow.EnvOpenAt, "2026-09-06 11:00")     // a Sunday
	t.Setenv(regwindow.EnvEventStart, "2026-10-03 09:30") // a Saturday
	t.Setenv(regwindow.EnvEventEnd, "2026-10-03 17:45")

	e := newTestEnv(t, true, 20)
	var got struct {
		OpenAt         int64  `json:"openAt"`
		EventStartAt   int64  `json:"eventStartAt"`
		EventEndAt     int64  `json:"eventEndAt"`
		OpenText       string `json:"openText"`
		OpenHowto      string `json:"openHowto"`
		OpenShort      string `json:"openShort"`
		OpenShortTime  string `json:"openShortTime"`
		EventText      string `json:"eventText"`
		EventShort     string `json:"eventShort"`
		EventShortTime string `json:"eventShortTime"`
		EventBadge     string `json:"eventBadge"`
		Limit          int    `json:"limit"` // pre-existing field must survive
	}
	if err := json.Unmarshal(e.get(t, "/api/count").Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OpenAt != regwindow.OpenAt().Unix() || got.EventStartAt != regwindow.EventStart().Unix() ||
		got.EventEndAt != regwindow.EventEnd().Unix() {
		t.Errorf("timestamps = %d/%d/%d; want %d/%d/%d", got.OpenAt, got.EventStartAt, got.EventEndAt,
			regwindow.OpenAt().Unix(), regwindow.EventStart().Unix(), regwindow.EventEnd().Unix())
	}
	for _, c := range []struct{ name, got, want string }{
		{"openText", got.OpenText, "niedziela 6 września 2026, 11:00 (czasu polskiego)"},
		{"openHowto", got.OpenHowto, "niedzielę 6 września, 11:00 czasu PL"},
		{"openShort", got.OpenShort, "Nd 6 września"},
		{"openShortTime", got.OpenShortTime, "11:00 czasu PL"},
		{"eventText", got.EventText, "sobota 3 października 2026, 09:30–17:45"},
		{"eventShort", got.EventShort, "Sob, 3 października"},
		{"eventShortTime", got.EventShortTime, "09:30 – 17:45"},
		{"eventBadge", got.EventBadge, "3 października 2026"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q; want %q", c.name, c.got, c.want)
		}
	}
	if got.Limit != 20 {
		t.Errorf("limit = %d; want 20 (existing fields must not regress)", got.Limit)
	}
}

// TestRegisterDuplicateOnWaitlist covers the ErrDuplicate branch for a handle
// that sits on the waiting list: re-registering with the same token renders the
// "duplicate" page in its waiting-list variant (WaitlistPos), not the
// confirmed-participant one.
func TestRegisterDuplicateOnWaitlist(t *testing.T) {
	e := newTestEnvSeats(t, true, 1, 2, "") // 1 confirmed seat, 2 waiting-list places

	// @a takes the only confirmed seat (#1).
	if rec := e.post(t, url.Values{"t": {e.token(t, "@a:hs.org")}, "city": {"Łódź"}, "email": {"a@example.com"}}); rec.Code != http.StatusOK {
		t.Fatalf("register @a status = %d", rec.Code)
	}

	tokB := e.token(t, "@b:hs.org")
	// @b lands on the waiting list: number 2 -> position #1.
	first := e.post(t, url.Values{"t": {tokB}, "city": {"Łódź"}, "email": {"b@example.com"}})
	if !strings.Contains(first.Body.String(), "rezerwow") || !strings.Contains(first.Body.String(), "#1") {
		t.Fatalf("@b first signup should be waitlist position #1: %s", first.Body.String())
	}

	// Re-registering @b with the same token hits ErrDuplicate: the duplicate
	// page must reference the waiting-list position, not a participant number.
	dup := e.post(t, url.Values{"t": {tokB}, "city": {"Kraków"}, "email": {"b@example.com"}})
	body := dup.Body.String()
	if !strings.Contains(body, "rezerwow") {
		t.Errorf("duplicate-on-waitlist body missing 'rezerwow': %s", body)
	}
	if !strings.Contains(body, "#1") {
		t.Errorf("duplicate-on-waitlist body missing waitlist position '#1': %s", body)
	}
	if strings.Contains(body, "numer uczestnika") {
		t.Errorf("duplicate-on-waitlist must NOT use the confirmed-participant wording: %s", body)
	}
	if n, _ := e.store.Count(); n != 2 {
		t.Fatalf("count = %d; want 2 (duplicate must not add a row)", n)
	}
}

// TestRootMethodNotAllowed verifies "/" answers 405 (Allow: GET) to a non-GET
// method.
func TestRootMethodNotAllowed(t *testing.T) {
	e := newTestEnv(t, true, 20)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d; want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET" {
		t.Errorf("POST / Allow = %q; want GET", allow)
	}
}

// TestRegisterMethodNotAllowed verifies /register rejects methods other than
// GET/POST with 405 (the handler's default switch branch).
func TestRegisterMethodNotAllowed(t *testing.T) {
	e := newTestEnv(t, true, 20)
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(m, "/register", nil)
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /register = %d; want 405", m, rec.Code)
		}
	}
}

// TestApiCountOpenFollowsTimeGate is the automatic-opening contract: with no
// REGISTRATION_OPEN override in play, /api/count reports open=false while
// REGISTRATION_OPEN_AT is in the future and open=true once it is in the past —
// and it does so per request, so a running server flips by itself with no
// restart. The landing page polls this endpoint and follows its flag.
func TestApiCountOpenFollowsTimeGate(t *testing.T) {
	t.Setenv("REGISTRATION_OPEN", "") // no override — the time gate decides
	sub, err := fs.Sub(embedded, ".")
	if err != nil {
		t.Fatalf("embed sub: %v", err)
	}
	// isOpen is wired exactly as main() wires it: the live regwindow gate.
	e := &testEnv{handler: newMux(deps{
		seatLimit:   20,
		isOpen:      regwindow.Open,
		files:       http.FS(sub),
		tokenSecret: testTokenSecret,
	})}
	for _, c := range []struct {
		openAt string
		want   bool
	}{
		{"2999-01-01 12:00", false},
		{"2000-01-01 12:00", true},
	} {
		t.Setenv(regwindow.EnvOpenAt, c.openAt)
		var got struct {
			Open bool `json:"open"`
		}
		if err := json.Unmarshal(e.get(t, "/api/count").Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Open != c.want {
			t.Errorf("REGISTRATION_OPEN_AT=%s → open=%v; want %v", c.openAt, got.Open, c.want)
		}
	}
}
