package matrixbot

import (
	"strings"
	"testing"

	"github.com/d33mobile/dday/internal/regwindow"
)

// TestHandleRegisterClosed asserts that when the registration gate is closed
// (IsOpen returns false), the bot does NOT open a DM: it replies publicly in
// the origin room with a "not started yet" notice carrying the start date and
// no registration link (no "?t=" token).
func TestHandleRegisterClosed(t *testing.T) {
	recipient, _ := genKeypair(t)

	created := make(chan map[string]any, 1)
	sent := make(chan map[string]any, 1)
	srv := newMockMatrix(t, created, sent)
	defer srv.Close()

	c := New(srv.URL)
	c.Recipient = recipient
	c.LinkBase = "https://dday.hs-ldz.pl/register"
	c.TokenSecret = testSecret
	c.IsOpen = func() bool { return false }
	if err := c.Login("@ddaybot:mock", "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}

	room, err := c.HandleRegister("!chan:mock", "@alice:mock")
	if err != nil {
		t.Fatalf("HandleRegister: %v", err)
	}
	if room != "" {
		t.Errorf("HandleRegister returned room %q, want \"\" (no DM opened)", room)
	}

	// No DM must have been created.
	select {
	case <-created:
		t.Fatal("closed registration must NOT create a DM room")
	default:
	}

	sendBody := <-sent
	body, _ := sendBody["body"].(string)
	if !strings.Contains(body, "nie wystartowały") && !strings.Contains(body, "jeszcze") {
		t.Errorf("message body = %q, want the 'not started yet' notice", body)
	}
	if !strings.Contains(body, regwindow.OpenStartText()) {
		t.Errorf("message body = %q, want the start date %q", body, regwindow.OpenStartText())
	}
	if strings.Contains(body, "?t=") {
		t.Errorf("message body = %q, must NOT contain a registration link (?t=)", body)
	}
	if formatted, _ := sendBody["formatted_body"].(string); strings.Contains(formatted, "?t=") {
		t.Errorf("formatted_body = %q, must NOT contain a registration link (?t=)", formatted)
	}
}
