package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

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
