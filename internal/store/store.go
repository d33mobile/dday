// Package store provides a thin SQLite persistence layer for event
// registrations, backed by the pure-Go modernc.org/sqlite driver (CGO off) so
// the binary stays fully static and runs on distroless images.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Sentinel errors returned by Register.
var (
	// ErrFull means the seat limit has been reached.
	ErrFull = errors.New("registration full")
	// ErrDuplicate means the Matrix handle is already registered; the returned
	// number is the existing participant number.
	ErrDuplicate = errors.New("already registered")
)

// Store wraps a SQLite database holding the registrations table.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS registrations (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	matrix_handle TEXT NOT NULL UNIQUE,
	nick          TEXT NOT NULL,
	city          TEXT NOT NULL,
	email         TEXT NOT NULL,
	created_at    INTEGER NOT NULL
);`

// Open opens (creating if needed) the SQLite database at path, applies sensible
// PRAGMAs and migrates the schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A single connection avoids "database is locked" under the file lock and
	// keeps PRAGMA settings effective for every query.
	db.SetMaxOpenConns(1)

	pragmas := []string{
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA foreign_keys = ON;",
		"PRAGMA synchronous = NORMAL;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", p, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Count returns the number of registrations.
func (s *Store) Count() (int, error) {
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM registrations").Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Number returns the participant number for handle and whether that handle is
// registered. An unregistered handle yields (0, false, nil); a lookup failure
// yields a non-nil error.
func (s *Store) Number(handle string) (int, bool, error) {
	var id int
	err := s.db.QueryRow("SELECT id FROM registrations WHERE matrix_handle = ?", handle).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, err
	default:
		return id, true, nil
	}
}

// Rank returns the 1-based position of handle among the CURRENT registrations
// ordered by id, and whether the handle is registered at all. Unlike Number
// (the immutable AUTOINCREMENT id shown to the participant), the rank shifts
// down when an earlier registration is deleted — that is what promotes the
// first waiting-list entry once a confirmed participant withdraws.
func (s *Store) Rank(handle string) (int, bool, error) {
	var rank int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM registrations
		WHERE id <= (SELECT id FROM registrations WHERE matrix_handle = ?)`,
		handle).Scan(&rank)
	switch {
	case err != nil:
		return 0, false, err
	case rank == 0:
		// The subquery yielded NULL: no row for this handle, so nothing counted.
		return 0, false, nil
	default:
		return rank, true, nil
	}
}

// Registration is one stored signup, as returned by List. It carries every
// column of the registrations table, including personal data — callers must
// treat it accordingly (it backs the admin view only).
type Registration struct {
	ID        int
	Handle    string
	Nick      string
	City      string
	Email     string
	CreatedAt int64 // unix seconds
}

// List returns every registration ordered by id, i.e. by signup order. The
// slice position (index+1) is the row's rank, so callers can derive the
// confirmed/waiting-list status without a per-row Rank query.
func (s *Store) List() ([]Registration, error) {
	rows, err := s.db.Query(
		"SELECT id, matrix_handle, nick, city, email, created_at FROM registrations ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Registration
	for rows.Next() {
		var r Registration
		if err := rows.Scan(&r.ID, &r.Handle, &r.Nick, &r.City, &r.Email, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Email returns the stored e-mail address for handle. It is used to verify
// that the web layer persists the normalized address rather than the raw form
// submission. A missing handle yields sql.ErrNoRows.
func (s *Store) Email(handle string) (string, error) {
	var email string
	err := s.db.QueryRow("SELECT email FROM registrations WHERE matrix_handle = ?", handle).Scan(&email)
	return email, err
}

// Register atomically records a new participant. It returns the assigned
// participant number (the row id). If the handle is already present it returns
// the existing number together with ErrDuplicate. If the seat limit is reached
// it returns ErrFull.
func (s *Store) Register(handle, nick, city, email string, limit int) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// Existing registration for this handle wins over everything else.
	var existing int
	err = tx.QueryRow("SELECT id FROM registrations WHERE matrix_handle = ?", handle).Scan(&existing)
	switch {
	case err == nil:
		return existing, ErrDuplicate
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}

	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM registrations").Scan(&count); err != nil {
		return 0, err
	}
	if count >= limit {
		return 0, ErrFull
	}

	res, err := tx.Exec(
		"INSERT INTO registrations (matrix_handle, nick, city, email, created_at) VALUES (?, ?, ?, ?, ?)",
		handle, nick, city, email, time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(id), nil
}

// Delete removes the registration for handle. It reports whether a row was
// actually removed (false means the handle was not registered). Deleting frees
// the seat, but the participant number (an AUTOINCREMENT id) is not reissued —
// that is acceptable for the GDPR erasure path.
func (s *Store) Delete(handle string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM registrations WHERE matrix_handle = ?", handle)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}
