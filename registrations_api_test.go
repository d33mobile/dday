package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testFeedToken = "s3cr3t-internal-token"

// feedGet issues GET /api/registrations with the given query and Authorization
// header, the two knobs the announcement feed's contract turns on.
func feedGet(t *testing.T, e *testEnv, query, auth string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/registrations"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// feedItem mirrors the JSON shape the bot decodes.
type feedItem struct {
	ID          int    `json:"id"`
	Handle      string `json:"handle"`
	Nick        string `json:"nick"`
	Rank        int    `json:"rank"`
	Confirmed   bool   `json:"confirmed"`
	WaitlistPos int    `json:"waitlistPos"`
}

func decodeFeed(t *testing.T, rec *httptest.ResponseRecorder) []feedItem {
	t.Helper()
	var out struct {
		Registrations []feedItem `json:"registrations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode feed %q: %v", rec.Body.String(), err)
	}
	return out.Registrations
}

// seedFeed registers two handles with personal data, so the tests can prove the
// feed does NOT echo any of it back.
func seedFeed(t *testing.T, e *testEnv) {
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

// TestRegistrationsFeedDisabledWithoutToken: an empty INTERNAL_TOKEN turns the
// endpoint off entirely (404), like /api/registered.
func TestRegistrationsFeedDisabledWithoutToken(t *testing.T) {
	e := newTestEnvToken(t, true, 20, "")
	seedFeed(t, e)
	if rec := feedGet(t, e, "", "Bearer "+testFeedToken); rec.Code != http.StatusNotFound {
		t.Errorf("feed on disabled endpoint = %d; want 404", rec.Code)
	}
}

// TestRegistrationsFeedRejectsBadToken: a missing or wrong bearer is 401 and
// leaks nothing.
func TestRegistrationsFeedRejectsBadToken(t *testing.T) {
	e := newTestEnvToken(t, true, 20, testFeedToken)
	seedFeed(t, e)

	for _, tc := range []struct{ name, auth string }{
		{"no credentials", ""},
		{"wrong bearer", "Bearer nope"},
		{"bare token", testFeedToken},
	} {
		rec := feedGet(t, e, "", tc.auth)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d; want 401", tc.name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "alice") {
			t.Errorf("%s leaked registration data", tc.name)
		}
	}
}

// TestRegistrationsFeedStatusFromRank: with one confirmed seat, #1 is a
// participant and #2 is waiting-list position 1 — the same rank rule as /admin.
func TestRegistrationsFeedStatusFromRank(t *testing.T) {
	e := newTestEnvSeats(t, true, 1, 20, testFeedToken)
	seedFeed(t, e)

	rec := feedGet(t, e, "since=0", "Bearer "+testFeedToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed = %d; want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q; want no-store", got)
	}
	items := decodeFeed(t, rec)
	if len(items) != 2 {
		t.Fatalf("got %d items; want 2", len(items))
	}
	if want := (feedItem{ID: 1, Handle: "@alice:hs.org", Nick: "alice", Rank: 1, Confirmed: true}); items[0] != want {
		t.Errorf("item[0] = %+v; want %+v", items[0], want)
	}
	if want := (feedItem{ID: 2, Handle: "@bob:hs.org", Nick: "bob", Rank: 2, WaitlistPos: 1}); items[1] != want {
		t.Errorf("item[1] = %+v; want %+v", items[1], want)
	}
}

// TestRegistrationsFeedFiltersSince: only ids strictly greater than since come
// back, so the bot never re-announces. A missing or unparsable since means 0.
func TestRegistrationsFeedFiltersSince(t *testing.T) {
	e := newTestEnvSeats(t, true, 20, 20, testFeedToken)
	seedFeed(t, e)

	for _, tc := range []struct {
		query   string
		wantIDs []int
	}{
		{"", []int{1, 2}},
		{"since=0", []int{1, 2}},
		{"since=1", []int{2}},
		{"since=2", nil},
		{"since=99", nil},
		{"since=abc", []int{1, 2}},
	} {
		rec := feedGet(t, e, tc.query, "Bearer "+testFeedToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q = %d; want 200", tc.query, rec.Code)
		}
		items := decodeFeed(t, rec)
		if len(items) != len(tc.wantIDs) {
			t.Fatalf("%q returned %d items; want %d", tc.query, len(items), len(tc.wantIDs))
		}
		for i, want := range tc.wantIDs {
			if items[i].ID != want {
				t.Errorf("%q item[%d].ID = %d; want %d", tc.query, i, items[i].ID, want)
			}
		}
	}
}

// TestRegistrationsFeedOmitsPersonalData is the GDPR guard: the announcement
// feed feeds a PUBLIC room, so the raw body must contain neither the e-mail nor
// the city of any registration — not as a field name, not as a value.
func TestRegistrationsFeedOmitsPersonalData(t *testing.T) {
	e := newTestEnvSeats(t, true, 20, 20, testFeedToken)
	seedFeed(t, e)

	rec := feedGet(t, e, "since=0", "Bearer "+testFeedToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed = %d; want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, forbidden := range []string{
		"email", "city", "alice@example.com", "bob@example.com", "Łódź", "Warszawa",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("feed body contains %q; personal data must never reach the announcement feed:\n%s",
				forbidden, body)
		}
	}
	// Sanity: it does carry what the announcement needs.
	for _, want := range []string{"@alice:hs.org", "alice", "waitlistPos"} {
		if !strings.Contains(body, want) {
			t.Errorf("feed body missing %q", want)
		}
	}
}

// TestRegistrationsFeedMethodNotAllowed: the feed is read-only.
func TestRegistrationsFeedMethodNotAllowed(t *testing.T) {
	e := newTestEnvToken(t, true, 20, testFeedToken)
	req := httptest.NewRequest(http.MethodPost, "/api/registrations", nil)
	req.Header.Set("Authorization", "Bearer "+testFeedToken)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d; want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET" {
		t.Errorf("Allow = %q; want GET", got)
	}
}
