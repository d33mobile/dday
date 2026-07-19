// Package regwindow is the shared registration time gate for both the web
// server and the Matrix bot. Keeping it in one place means the two binaries
// agree on exactly when registration opens.
package regwindow

import (
	"os"
	"time"
	_ "time/tzdata" // embed the tz database so Europe/Warsaw resolves on distroless
)

// OpenMoment is the instant registration opens: 2026-07-26 15:00 Europe/Warsaw.
var OpenMoment = time.Date(2026, 7, 26, 15, 0, 0, 0, Warsaw())

// Open reports whether registration is currently open. REGISTRATION_OPEN=1/true
// forces it open; otherwise it opens once we pass OpenMoment in the Warsaw
// timezone.
func Open() bool {
	switch os.Getenv("REGISTRATION_OPEN") {
	case "1", "true", "TRUE", "yes":
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
