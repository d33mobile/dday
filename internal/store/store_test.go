package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRegisterAndCount(t *testing.T) {
	s := openTest(t)

	if n, err := s.Count(); err != nil || n != 0 {
		t.Fatalf("initial count = %d, %v; want 0", n, err)
	}

	num, err := s.Register("@alice:hs.org", "alice", "Łódź", "a@example.com", 20)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if num != 1 {
		t.Fatalf("first number = %d; want 1", num)
	}
	if n, _ := s.Count(); n != 1 {
		t.Fatalf("count = %d; want 1", n)
	}

	num2, err := s.Register("@bob:hs.org", "bob", "Kraków", "b@example.com", 20)
	if err != nil {
		t.Fatalf("register bob: %v", err)
	}
	if num2 != 2 {
		t.Fatalf("second number = %d; want 2", num2)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	s := openTest(t)
	first, err := s.Register("@alice:hs.org", "alice", "Łódź", "a@example.com", 20)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	num, err := s.Register("@alice:hs.org", "alice", "Warszawa", "other@example.com", 20)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v; want ErrDuplicate", err)
	}
	if num != first {
		t.Fatalf("duplicate returned %d; want existing %d", num, first)
	}
	if n, _ := s.Count(); n != 1 {
		t.Fatalf("count = %d; want 1 (no new row)", n)
	}
}

func TestRegisterFull(t *testing.T) {
	s := openTest(t)
	if _, err := s.Register("@a:hs", "a", "Łódź", "a@x.com", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register("@b:hs", "b", "Łódź", "b@x.com", 2); err != nil {
		t.Fatal(err)
	}
	_, err := s.Register("@c:hs", "c", "Łódź", "c@x.com", 2)
	if !errors.Is(err, ErrFull) {
		t.Fatalf("err = %v; want ErrFull", err)
	}
	if n, _ := s.Count(); n != 2 {
		t.Fatalf("count = %d; want 2", n)
	}
}
