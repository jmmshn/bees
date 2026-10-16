package bee_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/bee"
)

// watch spawns bees-8 and starts watching it; the returned func waits for the watch.
func watch(t *testing.T, r rig) func() (bool, error) {
	t.Helper()
	bee.FastWatch(t)
	st, err := r.fleet.Spawn(context.Background(), "bees-8", "spawn and end")
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		ok, err := r.fleet.Watch(context.Background(), "bees-8", st.Spawn, "queen-bees")
		done <- result{ok, err}
	}()
	return func() (bool, error) {
		t.Helper()
		select {
		case res := <-done:
			return res.ok, res.err
		case <-time.After(5 * time.Second):
			t.Fatal("the watch never ended")
			return false, nil
		}
	}
}

func TestWatchTellsTheSpawnerWhenTheBeeExitsWithoutDone(t *testing.T) {
	r := newRig(t)
	wait := watch(t, r)
	time.Sleep(20 * time.Millisecond) // let the watcher pin the process
	r.h.exit("bees-8")
	if ok, err := wait(); !ok || err != nil {
		t.Fatalf("reported %v, %v; want reported", ok, err)
	}
	got, _ := r.lb.Unread(context.Background(), "queen-bees")
	if len(got) != 1 || got[0].From != "bs" || !strings.Contains(got[0].Body, "bees-8 exited without reporting done") || !strings.Contains(got[0].Body, "bs spawn bees-8") {
		t.Fatalf("queen's unread = %+v", got)
	}
}

func TestWatchIsQuietWhenTheBeeSaidDone(t *testing.T) {
	r := newRig(t)
	wait := watch(t, r)
	if _, err := r.lb.Send(context.Background(), "bees-8", "queen-bees", "done: bees-8 PR #4"); err != nil {
		t.Fatal(err)
	}
	r.h.exit("bees-8")
	if ok, err := wait(); ok || err != nil {
		t.Fatalf("reported %v, %v; want quiet", ok, err)
	}
}

func TestWatchEndsWithTheSpawn(t *testing.T) {
	r := newRig(t)
	wait := watch(t, r)
	if err := r.fleet.End(context.Background(), "bees-8"); err != nil { // the fake's Claude lives on
		t.Fatal(err)
	}
	if ok, err := wait(); ok || err != nil {
		t.Fatalf("reported %v, %v; want quiet", ok, err)
	}
}

func TestWatchIsQuietWhenEndingKillsTheBee(t *testing.T) {
	r := newRig(t)
	wait := watch(t, r)
	time.Sleep(20 * time.Millisecond)
	r.h.exit("bees-8") // bs end removes the workspace before it records the end
	if err := r.fleet.End(context.Background(), "bees-8"); err != nil {
		t.Fatal(err)
	}
	if ok, err := wait(); ok || err != nil {
		t.Fatalf("reported %v, %v; want quiet", ok, err)
	}
}

func TestWatchEndsWhenTheLogbookIsGone(t *testing.T) {
	r := newRig(t)
	wait := watch(t, r)
	if err := os.Remove(r.fleet.P.Logbook); err != nil {
		t.Fatal(err)
	}
	if ok, err := wait(); ok || err == nil {
		t.Fatalf("reported %v, %v; want an error", ok, err)
	}
}

func TestWatchTellsTheSpawnerWhenClaudeNeverShowsUp(t *testing.T) {
	r := newRig(t)
	wait := watch(t, r)
	r.h.exit("bees-8") // gone before the watcher's first look
	if ok, err := wait(); !ok || err != nil {
		t.Fatalf("reported %v, %v; want reported", ok, err)
	}
	got, _ := r.lb.Unread(context.Background(), "queen-bees")
	if len(got) != 1 || !strings.Contains(got[0].Body, "bees-8 never showed up") {
		t.Fatalf("queen's unread = %+v", got)
	}
}
