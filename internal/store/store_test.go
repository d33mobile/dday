package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
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

func TestNumber(t *testing.T) {
	s := openTest(t)

	if num, ok, err := s.Number("@alice:hs.org"); err != nil || ok || num != 0 {
		t.Fatalf("Number before register = (%d, %v, %v); want (0, false, nil)", num, ok, err)
	}

	want, err := s.Register("@alice:hs.org", "alice", "Łódź", "a@example.com", 20)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	num, ok, err := s.Number("@alice:hs.org")
	if err != nil || !ok || num != want {
		t.Fatalf("Number after register = (%d, %v, %v); want (%d, true, nil)", num, ok, err, want)
	}

	// A different, unregistered handle is still reported absent.
	if num, ok, _ := s.Number("@bob:hs.org"); ok || num != 0 {
		t.Fatalf("Number(unknown) = (%d, %v); want (0, false)", num, ok)
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

// TestRegisterWaitlistCapacity exercises the two-tier capacity model at the
// store level: with a combined limit of 40, the first 40 registrations all
// succeed with sequential numbers (1..40), the 41st is ErrFull, and Count never
// exceeds 40. The web layer classifies numbers 1..20 as confirmed and 21..40 as
// waiting list; here we only assert the store honors the total limit.
func TestRegisterWaitlistCapacity(t *testing.T) {
	s := openTest(t)
	const total = 40
	for i := 1; i <= total; i++ {
		h := fmt.Sprintf("@u%d:hs", i)
		num, err := s.Register(h, "u", "Łódź", "u@x.com", total)
		if err != nil {
			t.Fatalf("register #%d: %v", i, err)
		}
		if num != i {
			t.Fatalf("register #%d assigned number %d; want %d", i, num, i)
		}
	}
	if _, err := s.Register("@u41:hs", "u", "Łódź", "u@x.com", total); !errors.Is(err, ErrFull) {
		t.Fatalf("41st register err = %v; want ErrFull", err)
	}
	if n, _ := s.Count(); n != total {
		t.Fatalf("count = %d; want %d", n, total)
	}
}

// TestDelete covers the GDPR erasure path: deleting an existing handle removes
// the row and reports true; deleting a missing (or already deleted) handle
// reports false without error.
func TestDelete(t *testing.T) {
	s := openTest(t)
	if _, err := s.Register("@alice:hs.org", "alice", "Łódź", "a@example.com", 20); err != nil {
		t.Fatalf("register: %v", err)
	}

	deleted, err := s.Delete("@alice:hs.org")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Fatal("delete of existing handle = false; want true")
	}
	if n, _ := s.Count(); n != 0 {
		t.Fatalf("count after delete = %d; want 0", n)
	}

	// Deleting again (now absent) reports false, no error.
	deleted, err = s.Delete("@alice:hs.org")
	if err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if deleted {
		t.Fatal("delete of missing handle = true; want false")
	}

	// A never-registered handle likewise reports false.
	if deleted, err := s.Delete("@bob:hs.org"); err != nil || deleted {
		t.Fatalf("delete(unknown) = (%v, %v); want (false, nil)", deleted, err)
	}
}

// TestRank covers the rank lookup that drives participant-vs-waiting-list
// status: ranks are 1-based positions ordered by id, an unknown handle reports
// ok=false, and deleting a row shifts everyone behind it up one place while
// their participant numbers (ids) stay put.
func TestRank(t *testing.T) {
	s := openTest(t)
	for _, h := range []string{"@a:hs.org", "@b:hs.org", "@c:hs.org"} {
		if _, err := s.Register(h, "x", "Łódź", "x@example.com", 20); err != nil {
			t.Fatalf("register %s: %v", h, err)
		}
	}

	wantRank := func(handle string, want int) {
		t.Helper()
		rank, ok, err := s.Rank(handle)
		if err != nil {
			t.Fatalf("rank %s: %v", handle, err)
		}
		if !ok || rank != want {
			t.Errorf("rank(%s) = (%d, %v); want (%d, true)", handle, rank, ok, want)
		}
	}
	wantRank("@a:hs.org", 1)
	wantRank("@b:hs.org", 2)
	wantRank("@c:hs.org", 3)

	// An unknown handle has no rank.
	if rank, ok, err := s.Rank("@ghost:hs.org"); err != nil || ok || rank != 0 {
		t.Errorf("rank(unknown) = (%d, %v, %v); want (0, false, nil)", rank, ok, err)
	}

	// Deleting the first registration promotes the rest by one place.
	if _, err := s.Delete("@a:hs.org"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	wantRank("@b:hs.org", 1)
	wantRank("@c:hs.org", 2)

	// The participant number (id) is unaffected by the shift.
	if number, ok, err := s.Number("@c:hs.org"); err != nil || !ok || number != 3 {
		t.Errorf("number(@c) = (%d, %v, %v); want (3, true, nil)", number, ok, err)
	}

	// A deleted handle loses its rank.
	if rank, ok, err := s.Rank("@a:hs.org"); err != nil || ok || rank != 0 {
		t.Errorf("rank(deleted) = (%d, %v, %v); want (0, false, nil)", rank, ok, err)
	}
}

// TestList covers the admin listing: every column round-trips, rows come back
// in id order (so the slice index is the rank), and an empty table yields an
// empty slice rather than an error.
func TestList(t *testing.T) {
	s := openTest(t)

	if regs, err := s.List(); err != nil || len(regs) != 0 {
		t.Fatalf("List() on empty db = (%v, %v); want (empty, nil)", regs, err)
	}

	before := time.Now().Unix()
	for _, h := range []string{"@a:hs.org", "@b:hs.org", "@c:hs.org"} {
		if _, err := s.Register(h, "nick-"+h, "Łódź", h+"@example.com", 20); err != nil {
			t.Fatalf("register %s: %v", h, err)
		}
	}
	// Delete the middle row: List must skip it and keep the id order.
	if _, err := s.Delete("@b:hs.org"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	regs, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(regs) != 2 {
		t.Fatalf("len(List()) = %d; want 2", len(regs))
	}
	if regs[0].Handle != "@a:hs.org" || regs[1].Handle != "@c:hs.org" {
		t.Errorf("List() order = %q, %q; want @a, @c", regs[0].Handle, regs[1].Handle)
	}
	// ids are the immutable AUTOINCREMENT values, not the slice positions.
	if regs[0].ID != 1 || regs[1].ID != 3 {
		t.Errorf("List() ids = %d, %d; want 1, 3", regs[0].ID, regs[1].ID)
	}
	first := regs[0]
	if first.Nick != "nick-@a:hs.org" || first.City != "Łódź" || first.Email != "@a:hs.org@example.com" {
		t.Errorf("List()[0] fields = %+v", first)
	}
	if first.CreatedAt < before || first.CreatedAt > time.Now().Unix() {
		t.Errorf("List()[0].CreatedAt = %d; want within [%d, now]", first.CreatedAt, before)
	}
}
