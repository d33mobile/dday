package matrixbot

import (
	"strings"
	"testing"
)

// TestHandleRegisterClosed asserts that when the registration gate is closed
// (IsOpen returns false), the bot opens a DM and sends a "not open yet" notice
// containing no registration link (no "?t=" token), rather than a link.
func TestHandleRegisterClosed(t *testing.T) {
	recipient, _ := genKeypair(t)

	created := make(chan map[string]any, 1)
	sent := make(chan map[string]any, 1)
	srv := newMockMatrix(t, created, sent)
	defer srv.Close()

	c := New(srv.URL)
	c.Recipient = recipient
	c.LinkBase = "https://dday.hs-ldz.pl/register"
	c.IsOpen = func() bool { return false }
	if err := c.Login("@ddaybot:mock", "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}

	if _, err := c.HandleRegister("@alice:mock"); err != nil {
		t.Fatalf("HandleRegister: %v", err)
	}

	// A DM must have been created.
	select {
	case <-created:
	default:
		t.Fatal("expected a DM to be created")
	}

	sendBody := <-sent
	body, _ := sendBody["body"].(string)
	if !strings.Contains(body, "nie są jeszcze otwarte") {
		t.Errorf("message body = %q, want the 'not open yet' notice", body)
	}
	if strings.Contains(body, "?t=") {
		t.Errorf("message body = %q, must NOT contain a registration link (?t=)", body)
	}
	if formatted, _ := sendBody["formatted_body"].(string); strings.Contains(formatted, "?t=") {
		t.Errorf("formatted_body = %q, must NOT contain a registration link (?t=)", formatted)
	}
}
