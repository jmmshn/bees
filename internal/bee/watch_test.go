package bee_test

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/bee"
	"github.com/jmmshn/bees/internal/logbook"
)

// watcher is one running Watch: wait waits for it to return.
type watcher struct {
	told *atomic.Int32
	wait func() error
}

// watch starts watching the latest start of bees-8 under spawn.
func watch(t *testing.T, r rig, spawn logbook.ID, start string) watcher {
	t.Helper()
	w := watcher{told: new(atomic.Int32)}
	done := make(chan error, 1)
	go func() {
		done <- r.fleet.Watch(context.Background(), "bees-8", spawn, start, "queen-bees", func() { w.told.Add(1) })
	}()
	w.wait = func() error {
		t.Helper()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("the watch never ended")
			return nil
		}
	}
	return w
}

// spawnWatched spawns bees-8 and watches it.
func spawnWatched(t *testing.T, r rig) (watcher, logbook.ID) {
	t.Helper()
	bee.FastWatch(t)
	st, err := r.fleet.Spawn(context.Background(), "bees-8", "spawn and end")
	if err != nil {
		t.Fatal(err)
	}
	return watch(t, r, st.Spawn, "s1"), st.Spawn
}

// notices returns the bodies of the queen's unread messages.
func notices(t *testing.T, r rig) []string {
	t.Helper()
	got, err := r.lb.Unread(context.Background(), "queen-bees")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range got {
		if m.From != "bs" {
			t.Fatalf("unread from %s, want bs: %+v", m.From, m)
		}
		out = append(out, m.Body)
	}
	return out
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWatchTellsTheSpawnerWhenTheBeeExitsWithoutDone(t *testing.T) {
	r := newRig(t)
	w, _ := spawnWatched(t, r)
	time.Sleep(20 * time.Millisecond) // let the watcher pin the process
	r.h.exit("bees-8")
	if err := w.wait(); err != nil || w.told.Load() != 1 {
		t.Fatalf("told %d, %v; want told once", w.told.Load(), err)
	}
	got := notices(t, r)
	if len(got) != 1 || !strings.Contains(got[0], "bees-8 exited without reporting done") || !strings.Contains(got[0], "bs spawn bees-8") {
		t.Fatalf("notices = %q", got)
	}
}

func TestWatchIsQuietWhenTheBeeSaidDone(t *testing.T) {
	r := newRig(t)
	w, _ := spawnWatched(t, r)
	time.Sleep(20 * time.Millisecond)
	if _, err := r.lb.Send(context.Background(), "bees-8", "queen-bees", "done: bees-8 PR #4"); err != nil {
		t.Fatal(err)
	}
	r.h.exit("bees-8")
	if err := w.wait(); err != nil || w.told.Load() != 0 {
		t.Fatalf("told %d, %v; want quiet", w.told.Load(), err)
	}
}

func TestWatchEndsWithTheSpawn(t *testing.T) {
	r := newRig(t)
	w, _ := spawnWatched(t, r)
	if err := r.fleet.End(context.Background(), "bees-8"); err != nil { // the fake's Claude lives on
		t.Fatal(err)
	}
	if err := w.wait(); err != nil || w.told.Load() != 0 {
		t.Fatalf("told %d, %v; want quiet", w.told.Load(), err)
	}
}

func TestWatchIsQuietWhenEndingKillsTheBee(t *testing.T) {
	r := newRig(t)
	w, _ := spawnWatched(t, r)
	time.Sleep(20 * time.Millisecond)
	r.h.exit("bees-8") // bs end removes the workspace before it records the end
	if err := r.fleet.End(context.Background(), "bees-8"); err != nil {
		t.Fatal(err)
	}
	if err := w.wait(); err != nil || w.told.Load() != 0 {
		t.Fatalf("told %d, %v; want quiet", w.told.Load(), err)
	}
}

func TestWatchEndsWhenTheLogbookIsGone(t *testing.T) {
	r := newRig(t)
	w, _ := spawnWatched(t, r)
	if err := os.Remove(r.fleet.P.Logbook); err != nil {
		t.Fatal(err)
	}
	if err := w.wait(); err == nil || w.told.Load() != 0 {
		t.Fatalf("told %d, %v; want an error", w.told.Load(), err)
	}
}

func TestWatchTellsWhenClaudeIsNotRunningThenKeepsWatching(t *testing.T) {
	r := newRig(t)
	w, _ := spawnWatched(t, r)
	r.h.exit("bees-8") // gone before the watcher's first look
	eventually(t, "told not running", func() bool { return w.told.Load() == 1 })
	if got := notices(t, r); len(got) != 1 || !strings.Contains(got[0], "bees-8 not running") {
		t.Fatalf("notices = %q", got)
	}
	if _, err := r.fleet.Spawn(context.Background(), "bees-8", ""); err != nil { // a restart, same spawn
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond) // let the watcher pin the restarted process
	r.h.exit("bees-8")
	if err := w.wait(); err != nil || w.told.Load() != 2 {
		t.Fatalf("told %d, %v; want told twice", w.told.Load(), err)
	}
}

func TestRestartWhileTheOldWatcherIsAliveIsToldPerProcess(t *testing.T) {
	r := newRig(t)
	old, spawn := spawnWatched(t, r)
	time.Sleep(20 * time.Millisecond)
	r.h.exit("bees-8")
	if _, err := r.fleet.Spawn(context.Background(), "bees-8", ""); err != nil { // before the old watcher looks again
		t.Fatal(err)
	}
	if err := old.wait(); err != nil || old.told.Load() != 1 {
		t.Fatalf("old watcher told %d, %v; want its process's exit told", old.told.Load(), err)
	}
	next := watch(t, r, spawn, "s2")
	time.Sleep(20 * time.Millisecond)
	r.h.exit("bees-8")
	if err := next.wait(); err != nil || next.told.Load() != 1 {
		t.Fatalf("new watcher told %d, %v", next.told.Load(), err)
	}
	if got := notices(t, r); len(got) != 2 {
		t.Fatalf("notices = %q, want one per process", got)
	}
}
