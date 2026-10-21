package identity_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/identity"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
)

// self is this test process: an ancestor of itself, as a harness is of the bs it runs.
func self(t *testing.T) proc.Process {
	t.Helper()
	start, err := proc.Start(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return proc.Process{PID: os.Getpid(), Start: start}
}

// noHarness names a harness no ancestor runs: these tests may run inside a real Claude.
func noHarness(string) string { return "no-such-harness" }

// worker records an open spawn of name in pane, with a start of p unless p is zero.
func worker(t *testing.T, path, name, pane string, p proc.Process) logbook.ID {
	t.Helper()
	ctx := context.Background()
	lb, err := logbook.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	id, err := lb.RecordSpawn(ctx, "queen-x", logbook.Spawn{Name: name, Bead: name, Pane: pane, Harness: "claude_code"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.PID != 0 {
		if _, err := lb.RecordStart(ctx, logbook.Start{Spawn: id, Name: name, Process: p}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func find(t *testing.T, dir, pane string) (identity.Session, bool, error) {
	t.Helper()
	return identity.Find(context.Background(), dir, pane, noHarness)
}

// T9: a worker's pane names it while its recorded process is this bs's ancestor.
func TestAPaneNamesTheWorkerWhoseProcessIsAnAncestor(t *testing.T) {
	dir := t.TempDir()
	worker(t, filepath.Join(dir, "bees.db"), "bees-8", "w1:p1", self(t))
	worker(t, filepath.Join(dir, "bees.db"), "bees-9", "w2:p1", self(t)) // another pane
	s, ok, err := find(t, dir, "w1:p1")
	if err != nil || !ok || s.Project != "bees" || s.Name != "bees-8" || s.Logbook != filepath.Join(dir, "bees.db") || s.Queen {
		t.Fatalf("Find = %+v, %v, %v", s, ok, err)
	}
}

// T9: a stale open entry whose recorded process is gone names no one, and an ended spawn's
// pane names no one either (T4).
func TestAStaleOrEndedEntryNamesNoOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bees.db")
	stale := self(t)
	stale.Start += "0" // the pid reused by another process
	worker(t, path, "bees-8", "w1:p1", stale)
	id := worker(t, path, "bees-9", "w2:p1", self(t))
	lb, _ := logbook.Open(context.Background(), path)
	if err := lb.RecordEnd(context.Background(), "queen-bees", id); err != nil {
		t.Fatal(err)
	}
	lb.Close()
	for _, pane := range []string{"w1:p1", "w2:p1", "w9:p9"} {
		if s, ok, err := find(t, dir, pane); ok || err != nil {
			t.Errorf("Find(%s) = %+v, %v, %v; want no one", pane, s, ok, err)
		}
	}
}

// T9: the same pane recorded in two logbooks, both live, is an error naming both (motif 4).
func TestAPaneInTwoLogbooksIsAnError(t *testing.T) {
	dir := t.TempDir()
	worker(t, filepath.Join(dir, "bees.db"), "bees-8", "w1:p1", self(t))
	worker(t, filepath.Join(dir, "dotfiles.db"), "dotfiles-3", "w1:p1", self(t))
	if _, _, err := find(t, dir, "w1:p1"); !errors.Is(err, identity.ErrTwoSessions) {
		t.Fatalf("err = %v, want ErrTwoSessions", err)
	}
}

// T9: an unreadable .db fails closed, naming it; a copy aside (.db.pre-1) is never read.
func TestAnUnreadableLogbookIsAnErrorAndACopyAsideIsSkipped(t *testing.T) {
	dir := t.TempDir()
	worker(t, filepath.Join(dir, "bees.db"), "bees-8", "w1:p1", self(t))
	if err := os.WriteFile(filepath.Join(dir, "bees.db.pre-1"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := find(t, dir, "w1:p1"); !ok || err != nil {
		t.Fatalf("with a copy aside: ok=%v err=%v", ok, err)
	}
	bad := filepath.Join(dir, "broken.db")
	if err := os.WriteFile(bad, []byte("not a database at all, just text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := find(t, dir, "w1:p1"); err == nil || !errorsMention(err, bad) {
		t.Fatalf("err = %v, want an error naming %s", err, bad)
	}
}

func errorsMention(err error, s string) bool { return err != nil && strings.Contains(err.Error(), s) }

// A queen's pane names her only while her newest queen entry records it.
func TestAQueenIsNamedByHerNewestEntry(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	lb, err := logbook.Open(ctx, filepath.Join(dir, "bees.db"))
	if err != nil {
		t.Fatal(err)
	}
	gone := func(logbook.Queen) (bool, error) { return false, nil }
	me := self(t)
	if _, _, err := lb.RecordQueen(ctx, logbook.Queen{Name: "queen-bees", Pane: "w1:p1", PID: me.PID, Start: me.Start}, gone); err != nil {
		t.Fatal(err)
	}
	if s, ok, err := find(t, dir, "w1:p1"); err != nil || !ok || s.Name != "queen-bees" || !s.Queen || s.Project != "bees" {
		t.Fatalf("Find = %+v, %v, %v", s, ok, err)
	}
	if _, _, err := lb.RecordQueen(ctx, logbook.Queen{Name: "queen-bees", Pane: "w5:p1", PID: me.PID, Start: me.Start}, gone); err != nil {
		t.Fatal(err)
	}
	lb.Close()
	if _, ok, err := find(t, dir, "w1:p1"); ok || err != nil {
		t.Fatalf("her old pane: ok=%v err=%v; want no one", ok, err)
	}
}

// A worker's first bs call can come before its start is recorded: while an ancestor runs
// the harness and no entry recorded it, Find waits for the start.
func TestFindWaitsForAStartBeingConfirmed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bees.db")
	id := worker(t, path, "bees-8", "w1:p1", proc.Process{})
	name, err := proc.Name(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	asHarness := func(string) string { return name } // this test process plays the harness
	me := self(t)
	recorded := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		lb, err := logbook.Open(context.Background(), path)
		if err == nil {
			_, err = lb.RecordStart(context.Background(), logbook.Start{Spawn: id, Name: "bees-8", Process: me})
			lb.Close()
		}
		recorded <- err
	}()
	defer func() {
		if err := <-recorded; err != nil {
			t.Error(err)
		}
	}()
	s, ok, err := identity.Find(context.Background(), dir, "w1:p1", asHarness)
	if err != nil || !ok || s.Name != "bees-8" {
		t.Fatalf("Find = %+v, %v, %v; want bees-8 once its start was recorded", s, ok, err)
	}
	// With no harness among the ancestors (a keeper's shell), there is nothing to wait for.
	worker(t, filepath.Join(dir, "other.db"), "other-1", "w2:p1", proc.Process{})
	t0 := time.Now()
	if _, ok, err := find(t, dir, "w2:p1"); ok || err != nil || time.Since(t0) > time.Second {
		t.Fatalf("ok=%v err=%v after %s; want no one at once", ok, err, time.Since(t0))
	}
}

// An unrecorded harness that started longer than Wait ago (launched by a bs from before
// start entries) is never waited for: its bs calls fall back at once.
func TestFindDoesNotWaitForAnOldHarness(t *testing.T) {
	dir := t.TempDir()
	worker(t, filepath.Join(dir, "bees.db"), "bees-8", "w1:p1", proc.Process{})
	name, err := proc.Name(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	saved := identity.Wait
	identity.Wait = time.Millisecond // this test process is older than that
	t.Cleanup(func() { identity.Wait = saved })
	t0 := time.Now()
	if _, ok, err := identity.Find(context.Background(), dir, "w1:p1", func(string) string { return name }); ok || err != nil || time.Since(t0) > time.Second {
		t.Fatalf("ok=%v err=%v after %s; want no one at once", ok, err, time.Since(t0))
	}
}

// A .db that Open is still creating (no entries table yet) records nothing; it is no error.
func TestANewEmptyLogbookRecordsNothing(t *testing.T) {
	dir := t.TempDir()
	worker(t, filepath.Join(dir, "bees.db"), "bees-8", "w1:p1", self(t))
	if err := os.WriteFile(filepath.Join(dir, "new.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if s, ok, err := find(t, dir, "w1:p1"); err != nil || !ok || s.Name != "bees-8" {
		t.Fatalf("Find = %+v, %v, %v", s, ok, err)
	}
}
