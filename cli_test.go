package main

import (
	"path/filepath"
	"testing"

	"github.com/d33mobile/dday/internal/store"
)

// TestDeleteHandle covers the GDPR erasure core (deleteHandle) end to end on a
// temporary database: deleting an existing registration reports deleted=true,
// and a second delete of the same handle reports deleted=false.
func TestDeleteHandle(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cli.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := st.Register("@alice:hs.org", "alice", "Łódź", "a@example.com", 20); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	deleted, err := deleteHandle(dbPath, "@alice:hs.org")
	if err != nil {
		t.Fatalf("deleteHandle: %v", err)
	}
	if !deleted {
		t.Error("first delete should report deleted=true")
	}

	deleted, err = deleteHandle(dbPath, "@alice:hs.org")
	if err != nil {
		t.Fatalf("deleteHandle (second): %v", err)
	}
	if deleted {
		t.Error("second delete should report deleted=false (already gone)")
	}
}

// TestDeleteHandleUnopenableStore verifies a store that cannot be opened (its
// parent directory does not exist) surfaces as an error rather than a panic.
func TestDeleteHandleUnopenableStore(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "no-such-dir", "x.db")
	if _, err := deleteHandle(bad, "@x:hs.org"); err == nil {
		t.Error("expected an error opening a store under a nonexistent directory")
	}
}
