package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testAdminToken = "s3cr3t-admin-token"

// adminGet issues GET /admin, optionally with a query token and/or an
// Authorization header, mirroring the two ways an operator can authenticate.
func adminGet(t *testing.T, e *testEnv, query, auth string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/admin"
	if query != "" {
		path += "?t=" + url.QueryEscape(query)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// seedAdmin registers n handles so the admin view has rows to render.
func seedAdmin(t *testing.T, e *testEnv) {
	t.Helper()
	rows := []struct{ handle, nick, city, email string }{
		{"@alice:hs.org", "alice", "Łódź", "alice@example.com"},
		{"@bob:hs.org", "bob", "Warszawa", "bob@example.com"},
	}
	for _, r := range rows {
		if _, err := e.store.Register(r.handle, r.nick, r.city, r.email, 100); err != nil {
			t.Fatalf("seed %s: %v", r.handle, err)
		}
	}
}

// TestAdminDisabledWithoutToken: an empty ADMIN_TOKEN turns the endpoint off
// entirely, so its existence is not even advertised.
func TestAdminDisabledWithoutToken(t *testing.T) {
	e := newTestEnvAdmin(t, 20, 20, "")
	if rec := adminGet(t, e, testAdminToken, ""); rec.Code != http.StatusNotFound {
		t.Errorf("query token on disabled endpoint = %d; want 404", rec.Code)
	}
	if rec := adminGet(t, e, "", "Bearer "+testAdminToken); rec.Code != http.StatusNotFound {
		t.Errorf("bearer on disabled endpoint = %d; want 404", rec.Code)
	}
}

// TestAdminRejectsBadToken: no token and a wrong token are both 401, whether
// presented in the query string or the Authorization header.
func TestAdminRejectsBadToken(t *testing.T) {
	e := newTestEnvAdmin(t, 20, 20, testAdminToken)
	seedAdmin(t, e)

	for _, tc := range []struct{ name, query, auth string }{
		{"no credentials", "", ""},
		{"wrong query token", "nope", ""},
		{"wrong bearer", "", "Bearer nope"},
		{"bare token as header", "", testAdminToken},
	} {
		rec := adminGet(t, e, tc.query, tc.auth)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d; want 401", tc.name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "alice") {
			t.Errorf("%s leaked registration data", tc.name)
		}
	}
}

// TestAdminShowsRegistrations: with a valid token (either transport) the view
// renders every stored field, plus a no-store cache directive.
func TestAdminShowsRegistrations(t *testing.T) {
	e := newTestEnvAdmin(t, 20, 20, testAdminToken)
	seedAdmin(t, e)

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"query token": adminGet(t, e, testAdminToken, ""),
		"bearer":      adminGet(t, e, "", "Bearer "+testAdminToken),
	} {
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d; want 200", name, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s Cache-Control = %q; want no-store", name, got)
		}
		body := rec.Body.String()
		for _, want := range []string{
			"alice", "@alice:hs.org", "Łódź", "alice@example.com",
			"bob", "@bob:hs.org", "Warszawa", "bob@example.com",
			"uczestnik", "Panel admina",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s body missing %q", name, want)
			}
		}
	}
}

// TestAdminStatusFromRank: with a single confirmed seat the first signup is a
// participant and the second lands on the waiting list at position #1 — the
// status comes from the rank, exactly like /panel.
func TestAdminStatusFromRank(t *testing.T) {
	e := newTestEnvAdmin(t, 1, 3, testAdminToken)
	seedAdmin(t, e)

	body := adminGet(t, e, testAdminToken, "").Body.String()
	if !strings.Contains(body, "uczestnik") {
		t.Errorf("body missing 'uczestnik' for the first signup: %s", body)
	}
	if !strings.Contains(body, "rezerwa #1") {
		t.Errorf("body missing 'rezerwa #1' for the second signup: %s", body)
	}
	// Summary counts follow the same split.
	if !strings.Contains(body, "1/1") || !strings.Contains(body, "1/3") {
		t.Errorf("summary missing 1/1 confirmed and 1/3 waitlist: %s", body)
	}
}

// TestAdminEmpty: an empty database renders the placeholder, not a table.
func TestAdminEmpty(t *testing.T) {
	e := newTestEnvAdmin(t, 20, 20, testAdminToken)
	rec := adminGet(t, e, testAdminToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Brak zgłoszeń") {
		t.Errorf("body missing 'Brak zgłoszeń': %s", body)
	}
	if strings.Contains(body, "<table>") {
		t.Errorf("empty view must not render a table")
	}
}

// TestAdminMethodNotAllowed: the view is read-only, so anything but GET is 405.
func TestAdminMethodNotAllowed(t *testing.T) {
	e := newTestEnvAdmin(t, 20, 20, testAdminToken)
	req := httptest.NewRequest(http.MethodPost, "/admin?t="+url.QueryEscape(testAdminToken), nil)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d; want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET" {
		t.Errorf("Allow = %q; want GET", got)
	}
}

// TestAdminEscapesUserData: nick/city/e-mail come from user input, so the
// template must escape them rather than emit raw HTML.
func TestAdminEscapesUserData(t *testing.T) {
	e := newTestEnvAdmin(t, 20, 20, testAdminToken)
	if _, err := e.store.Register("@mallory:hs.org", "<script>x</script>", "Łódź", "m@example.com", 100); err != nil {
		t.Fatalf("register: %v", err)
	}
	body := adminGet(t, e, testAdminToken, "").Body.String()
	if strings.Contains(body, "<script>x</script>") {
		t.Errorf("nick rendered unescaped: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("escaped nick not found: %s", body)
	}
}
