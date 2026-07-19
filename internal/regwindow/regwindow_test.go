package regwindow

import "testing"

func TestOpenEnvOverride(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes"} {
		t.Setenv("REGISTRATION_OPEN", v)
		if !Open() {
			t.Errorf("Open() with REGISTRATION_OPEN=%q = false, want true", v)
		}
	}
}

// TestOpenDefaultBeforeMoment asserts that without an override, the gate is
// closed while the current time is before OpenMoment (2026-07-26 15:00). This
// test is valid until that moment passes; when it does, flip the expectation.
func TestOpenDefaultBeforeMoment(t *testing.T) {
	t.Setenv("REGISTRATION_OPEN", "")
	if Open() {
		t.Errorf("Open() before OpenMoment = true, want false (now is past %v?)", OpenMoment)
	}
}

func TestWarsawIsCEST(t *testing.T) {
	loc := Warsaw()
	if loc == nil {
		t.Fatal("Warsaw() returned nil")
	}
	// The OpenMoment is in summer, so the offset must be +02:00 (7200s).
	_, offset := OpenMoment.Zone()
	if offset != 2*60*60 {
		t.Errorf("OpenMoment offset = %ds, want 7200s", offset)
	}
}
