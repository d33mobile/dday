package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/d33mobile/dday/internal/matrixbot"
)

// panelGet fetches /panel with a fresh panel-kind token for handle.
func (e *testEnv) panelGet(t *testing.T, handle string) *httptest.ResponseRecorder {
	t.Helper()
	return e.get(t, "/panel?t="+url.QueryEscape(e.tokenKind(t, handle, matrixbot.KindPanel)))
}

// panelWithdraw posts the withdrawal action with a fresh panel-kind token.
func (e *testEnv) panelWithdraw(t *testing.T, handle string) *httptest.ResponseRecorder {
	t.Helper()
	return e.postTo(t, "/panel", url.Values{"t": {e.tokenKind(t, handle, matrixbot.KindPanel)}})
}

// seed registers handle directly in the store, bypassing the web layer.
func (e *testEnv) seed(t *testing.T, handle string) {
	t.Helper()
	if _, err := e.store.Register(handle, nickFromHandle(handle), "Łódź", "x@example.com", 100); err != nil {
		t.Fatalf("seed %s: %v", handle, err)
	}
}

// TestPanelGetShowsStatusAndAction verifies the panel renders the participant
// number, the confirmed status and the single available action.
func TestPanelGetShowsStatusAndAction(t *testing.T) {
	e := newTestEnv(t, true, 20)
	e.seed(t, "@alice:hs.org")

	rec := e.panelGet(t, "@alice:hs.org")
	if rec.Code != http.StatusOK {
		t.Fatalf("panel GET status = %d; want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"alice", "#1", "uczestnik", "Wycofaj udział", `action="/panel"`, "/privacy"} {
		if !strings.Contains(body, want) {
			t.Errorf("panel body missing %q: %s", want, body)
		}
	}
}

// TestPanelGetWaitlistStatus verifies a waiting-list participant sees their
// waiting-list position rather than the confirmed wording.
func TestPanelGetWaitlistStatus(t *testing.T) {
	e := newTestEnvSeats(t, true, 1, 3, "")
	e.seed(t, "@a:hs.org")
	e.seed(t, "@b:hs.org")

	body := e.panelGet(t, "@b:hs.org").Body.String()
	if !strings.Contains(body, "Status: <b>lista rezerwowa</b>, pozycja #1") {
		t.Errorf("second signup should show waiting-list position #1: %s", body)
	}
	if !strings.Contains(body, "#2") {
		t.Errorf("waiting-list panel should still show participant number #2: %s", body)
	}
}

// TestPanelGetNotRegistered verifies a valid panel token for a handle with no
// registration yields the neutral "not registered" page with the !start hint.
func TestPanelGetNotRegistered(t *testing.T) {
	e := newTestEnv(t, true, 20)

	body := e.panelGet(t, "@ghost:hs.org").Body.String()
	if !strings.Contains(body, "Nie jesteś zapisany") {
		t.Errorf("panel body missing 'Nie jesteś zapisany': %s", body)
	}
	if !strings.Contains(body, "!start") {
		t.Errorf("panel body missing the !start hint: %s", body)
	}
	if strings.Contains(body, "Wycofaj udział") {
		t.Errorf("unregistered panel must not offer the withdrawal action")
	}
}

// TestPanelPostWithdraws verifies the withdrawal removes the registration, and
// that a repeated POST is idempotent and neutral.
func TestPanelPostWithdraws(t *testing.T) {
	e := newTestEnv(t, true, 20)
	e.seed(t, "@alice:hs.org")

	rec := e.panelWithdraw(t, "@alice:hs.org")
	if rec.Code != http.StatusOK {
		t.Fatalf("withdraw status = %d; want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "wycofan") {
		t.Errorf("withdraw body missing 'wycofan': %s", rec.Body.String())
	}
	if n, _ := e.store.Count(); n != 0 {
		t.Fatalf("count = %d; want 0 after withdrawal", n)
	}

	// Second withdrawal: neutral message, still nothing stored.
	rec2 := e.panelWithdraw(t, "@alice:hs.org")
	if !strings.Contains(rec2.Body.String(), "Nie jesteś zapisany") {
		t.Errorf("repeat withdraw body missing 'Nie jesteś zapisany': %s", rec2.Body.String())
	}
	if n, _ := e.store.Count(); n != 0 {
		t.Fatalf("count = %d; want 0 after repeat withdrawal", n)
	}
}

// TestTokenKindsAreSeparate is the security-relevant test: a registration token
// must not open the panel, and a panel token must not drive registration.
func TestTokenKindsAreSeparate(t *testing.T) {
	e := newTestEnv(t, true, 20)
	const handle = "@alice:hs.org"
	e.seed(t, handle)

	regTok := e.tokenKind(t, handle, matrixbot.KindReg)
	panelTok := e.tokenKind(t, handle, matrixbot.KindPanel)

	// reg token on /panel (GET and POST).
	rec := e.get(t, "/panel?t="+url.QueryEscape(regTok))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Nieprawidłowy link") {
		t.Errorf("reg token on GET /panel = %d, body: %s; want 400 'Nieprawidłowy link'", rec.Code, rec.Body.String())
	}
	rec = e.postTo(t, "/panel", url.Values{"t": {regTok}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("reg token on POST /panel = %d; want 400", rec.Code)
	}
	if n, _ := e.store.Count(); n != 1 {
		t.Fatalf("count = %d; want 1 (a reg token must never withdraw)", n)
	}

	// panel token on /register (GET and POST).
	rec = e.get(t, "/register?t="+url.QueryEscape(panelTok))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Nieprawidłowy link") {
		t.Errorf("panel token on GET /register = %d, body: %s; want 400", rec.Code, rec.Body.String())
	}
	rec = e.post(t, url.Values{"t": {panelTok}, "city": {"Łódź"}, "email": {"a@example.com"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Nieprawidłowy link") {
		t.Errorf("panel token on POST /register = %d, body: %s; want 400", rec.Code, rec.Body.String())
	}
}

// TestPanelExpiredToken verifies the panel honors the same TTL as registration.
func TestPanelExpiredToken(t *testing.T) {
	e := newTestEnv(t, true, 20)
	e.seed(t, "@alice:hs.org")
	old := time.Now().Add(-49 * time.Hour).Unix()
	tok := e.tokenKindAt(t, "@alice:hs.org", matrixbot.KindPanel, old)

	rec := e.get(t, "/panel?t="+url.QueryEscape(tok))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expired panel token = %d; want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "wygasł") {
		t.Errorf("expired panel body missing 'wygasł': %s", rec.Body.String())
	}
	if n, _ := e.store.Count(); n != 1 {
		t.Fatalf("count = %d; want 1 (expired token must not withdraw)", n)
	}
}

// TestPanelMethodNotAllowed verifies /panel is GET+POST only.
func TestPanelMethodNotAllowed(t *testing.T) {
	e := newTestEnv(t, true, 20)
	req := httptest.NewRequest(http.MethodPut, "/panel", nil)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /panel = %d; want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, POST" {
		t.Errorf("PUT /panel Allow = %q; want %q", allow, "GET, POST")
	}
}

// TestPanelWithdrawPromotesWaitlist is the rank test: with a single confirmed
// seat, @a is the participant and @b sits on the waiting list. Once @a
// withdraws through the panel, @b must be classified as a confirmed
// participant — proving status comes from the current rank, not the row id
// (@b keeps participant number #2 either way).
func TestPanelWithdrawPromotesWaitlist(t *testing.T) {
	e := newTestEnvSeats(t, true, 1, 2, "")
	e.seed(t, "@a:hs.org")
	e.seed(t, "@b:hs.org")

	// Before: @b is on the waiting list at position #1. Match the status line,
	// not the page's static footer copy (which also says "rezerwowej").
	const waitStatus = "Status: <b>lista rezerwowa</b>, pozycja #1"
	if body := e.panelGet(t, "@b:hs.org").Body.String(); !strings.Contains(body, waitStatus) {
		t.Fatalf("@b should start on the waiting list: %s", body)
	}

	if rec := e.panelWithdraw(t, "@a:hs.org"); rec.Code != http.StatusOK {
		t.Fatalf("@a withdraw status = %d", rec.Code)
	}

	// After: @b is promoted to a confirmed participant, keeping number #2.
	body := e.panelGet(t, "@b:hs.org").Body.String()
	if strings.Contains(body, waitStatus) {
		t.Errorf("@b should be promoted off the waiting list: %s", body)
	}
	if !strings.Contains(body, "Status: <b>uczestnik</b>") {
		t.Errorf("@b should be a confirmed participant: %s", body)
	}
	if !strings.Contains(body, "#2") {
		t.Errorf("@b should keep participant number #2: %s", body)
	}

	// The same promotion is visible on the registration duplicate page.
	dup := e.post(t, url.Values{"t": {e.token(t, "@b:hs.org")}, "city": {"Łódź"}, "email": {"b@example.com"}})
	if dbody := dup.Body.String(); !strings.Contains(dbody, "Jesteś już zapisany") || strings.Contains(dbody, "rezerwow") {
		t.Errorf("duplicate page should show @b as confirmed: %s", dbody)
	}
}
