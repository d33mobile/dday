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

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}
