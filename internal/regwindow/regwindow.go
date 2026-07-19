// Package regwindow is the shared registration time gate for both the web
// server and the Matrix bot. Keeping it in one place means the two binaries
// agree on exactly when registration opens.
package regwindow

import (
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // embed the tz database so Europe/Warsaw resolves on distroless
)

// OpenMoment is the instant registration opens: 2026-07-26 15:00 Europe/Warsaw.
var OpenMoment = time.Date(2026, 7, 26, 15, 0, 0, 0, Warsaw())

// OpenStartText is the human-readable rendering of OpenMoment, shared by the web
// server's "closed"/"expired" pages and the bot's "not open yet" DM so the date
// is stated in exactly one place. Keep it in sync with OpenMoment above.
const OpenStartText = "niedziela 26 lipca 2026, 15:00 (czasu polskiego)"

// Open reports whether registration is currently open. A truthy
// REGISTRATION_OPEN (anything strconv.ParseBool reads as true — 1/t/T/TRUE/
// true/True — plus "yes") forces it open. Any other value, including a falsy or
// unparseable one, falls through to the time gate: open once we pass OpenMoment
// in the Warsaw timezone.
func Open() bool {
	v := strings.TrimSpace(os.Getenv("REGISTRATION_OPEN"))
	if b, err := strconv.ParseBool(v); err == nil && b {
		return true
	}
	if strings.EqualFold(v, "yes") {
		return true
	}
	return !time.Now().Before(OpenMoment)
}

// Warsaw returns the Europe/Warsaw location, falling back to a fixed +02:00
// (CEST) zone if the tz database is unavailable. time/tzdata is imported so the
// lookup succeeds even on distroless images without system tzdata.
func Warsaw() *time.Location {
	loc, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		return time.FixedZone("CEST", 2*60*60)
	}
	return loc
}
