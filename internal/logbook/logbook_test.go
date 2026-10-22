package logbook_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/logbook"
)

func spawn(t *testing.T, lb *logbook.Logbook, name, session, pane string, goals ...string) logbook.Spawn {
	t.Helper()
	g, err := goal.Parse(goals)
	if err != nil {
		t.Fatal(err)
	}
	s := logbook.Spawn{Name: name, Bead: name, Harness: "claude_code", Goal: g,
		Herdr: logbook.Pane{Session: session, Workspace: strings.Split(pane, ":")[0], Pane: pane}}
	if s.ID, err = lb.RecordSpawn(context.Background(), "queen-toy", s, "brief for "+name); err != nil {
		t.Fatal(err)
	}
	return s
}

// A v2 spawn reads back as written, goal included.
func TestASpawnRoundTrips(t *testing.T) {
	ctx := context.Background()
	lb := open(t, filepath.Join(t.TempDir(), "bees.db"))
	s := spawn(t, lb, "toy-1", "toy", "w1:p1", "pr:draft", "verdict")
	got, ok, err := lb.LatestSpawn(ctx, "toy-1")
	if err != nil || !ok || got.Legacy() || got.Goal.String() != "pr:draft verdict" || got.Herdr != s.Herdr || got.By != "queen-toy" {
		t.Fatalf("spawn = %+v, %v, %v", got, ok, err)
	}
}

// Identity is keyed by (session, pane): a herdr --session server numbers its panes from
// w1:p1 too, so the same pane id on two servers is two sessions (ADR-0010).
func TestInPaneKeysOnTheHerdrSession(t *testing.T) {
	ctx := context.Background()
	lb := open(t, filepath.Join(t.TempDir(), "bees.db"))
	a := spawn(t, lb, "toy-1", "default", "w1:p1", "branch")
	b := spawn(t, lb, "toy-2", "toy", "w1:p1", "branch")
	for session, want := range map[string]logbook.ID{"default": a.ID, "toy": b.ID} {
		in, err := lb.InPane(ctx, session, "w1:p1")
		if err != nil || len(in) != 1 || in[0].Spawn != want {
			t.Fatalf("InPane(%s) = %+v, %v; want spawn %s", session, in, err, want)
		}
	}
	if in, _ := lb.InPane(ctx, "other", "w1:p1"); len(in) != 0 {
		t.Fatalf("another server's pane matched: %+v", in)
	}
	// An ended spawn no longer holds its pane; a queen's newest entry does.
	if err := lb.RecordEnd(ctx, "queen-toy", a.ID, logbook.Ended{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := lb.RecordQueen(ctx, logbook.Queen{Name: "queen-toy", Session: "default", Pane: "w1:p1", Process: logbook.Process{PID: 5, Start: "s"}}, nil); err != nil {
		t.Fatal(err)
	}
	in, err := lb.InPane(ctx, "default", "w1:p1")
	if err != nil || len(in) != 1 || !in[0].Queen || in[0].Process.PID != 5 {
		t.Fatalf("after end: %+v, %v", in, err)
	}
}

// Of queens starting at once, one is recorded and the rest find her running (motif 7).
func TestOneOfConcurrentQueensIsRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bees.db")
	var wg sync.WaitGroup
	var mu sync.Mutex
	recorded := 0
	for pid := range 8 {
		wg.Go(func() {
			lb, _, err := logbook.Create(context.Background(), path)
			if err != nil {
				t.Error(err)
				return
			}
			defer lb.Close()
			_, ok, err := lb.RecordQueen(context.Background(), logbook.Queen{Name: "queen-toy", Process: logbook.Process{PID: pid + 1}}, func(logbook.Queen) (bool, error) { return true, nil })
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if ok {
				recorded++
			}
		})
	}
	wg.Wait()
	if recorded != 1 {
		t.Fatalf("%d queens recorded, want 1", recorded)
	}
}

// An amend replaces the goal and its waivers; waivers accumulate; a discard sticks.
func TestGoalEntriesAmendWaiveAndDiscard(t *testing.T) {
	ctx := context.Background()
	lb := open(t, filepath.Join(t.TempDir(), "bees.db"))
	s := spawn(t, lb, "toy-1", "toy", "w1:p1", "pr")
	for _, c := range []logbook.GoalChange{{By: "queen-toy", Waive: goal.CIGreen}, {By: "queen-toy", Waive: goal.CIGreen}} {
		if _, err := lb.RecordGoal(ctx, s.ID, s.Name, c); err != nil {
			t.Fatal(err)
		}
	}
	e, err := lb.Goal(ctx, s)
	if err != nil || e.String() != "pr" || len(e.Waived) != 1 || e.Discarded {
		t.Fatalf("after waivers: %+v, %v", e, err)
	}
	draft, _ := goal.Parse([]string{"pr:draft"})
	for _, c := range []logbook.GoalChange{{By: "queen-toy", Goal: &draft}, {By: "keeper", Discard: true}} {
		if _, err := lb.RecordGoal(ctx, s.ID, s.Name, c); err != nil {
			t.Fatal(err)
		}
	}
	if e, err = lb.Goal(ctx, s); err != nil || e.String() != "pr:draft" || len(e.Waived) != 0 || !e.Discarded {
		t.Fatalf("after amend and discard: %+v, %v", e, err)
	}
}

// RecordSpawn refuses a twin under its own write lock: a worker whose spawn hasn't ended
// can't be recorded again until it ends.
func TestRecordSpawnRefusesAnOpenTwin(t *testing.T) {
	ctx := context.Background()
	lb := open(t, filepath.Join(t.TempDir(), "bees.db"))
	s := spawn(t, lb, "toy-1", "toy", "w1:p1", "verdict")
	if _, err := lb.RecordSpawn(ctx, "queen-toy", s, ""); !errors.Is(err, logbook.ErrAlreadySpawned) {
		t.Fatalf("twin: err = %v, want ErrAlreadySpawned", err)
	}
	if err := lb.RecordEnd(ctx, "queen-toy", s.ID, logbook.Ended{}); err != nil {
		t.Fatal(err)
	}
	spawn(t, lb, "toy-1", "toy", "w2:p1", "verdict")
}

// A done is the queen's unread mail, still after its worker ended; the newest wins.
// landed (with the done, in its transaction) and ended are written once per spawn.
func TestDoneLandedAndEnded(t *testing.T) {
	ctx := context.Background()
	lb := open(t, filepath.Join(t.TempDir(), "bees.db"))
	s := spawn(t, lb, "toy-1", "toy", "w1:p1", "verdict")
	d := logbook.Done{Spawn: s.ID, Name: s.Name, Queen: "queen-toy", Bead: s.Bead, Answer: "yes", Report: "it holds",
		Checks: []logbook.CheckResult{{Name: goal.NoBackground, Pass: true}, {Name: goal.VerdictGiven, Pass: true}}}
	if _, err := lb.RecordDone(ctx, d, &logbook.Landed{By: "done"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := lb.RecordLanded(ctx, s.ID, s.Name, s.Name, logbook.Landed{By: "done"}); err != nil {
			t.Fatal(err)
		}
		if err := lb.RecordEnd(ctx, "queen-toy", s.ID, logbook.Ended{Force: true}); err != nil {
			t.Fatal(err)
		}
	}
	if ld, ok, err := lb.LandedOf(ctx, s.ID); err != nil || !ok || ld.By != "done" {
		t.Fatalf("landed = %+v, %v, %v", ld, ok, err)
	}
	if e, ok, err := lb.EndOf(ctx, s.ID); err != nil || !ok || !e.Force {
		t.Fatalf("ended = %+v, %v, %v", e, ok, err)
	}
	unread, err := lb.Unread(ctx, "queen-toy")
	if err != nil || len(unread) != 1 || !strings.HasPrefix(unread[0].Body, "done: toy-1 answer yes\nit holds") {
		t.Fatalf("queen's unread = %+v, %v", unread, err)
	}
	if got, ok, _ := lb.LatestDone(ctx, s.ID); !ok || !got.Passed() {
		t.Fatalf("done = %+v", got)
	}
	// The brief of an ended spawn is no longer unread.
	if unread, _ := lb.Unread(ctx, "toy-1"); len(unread) != 0 {
		t.Fatalf("worker's unread after end = %+v", unread)
	}
}

// A worker's mail is scoped at read time to its open spawn (bees-13.9, B): what was sent
// while it ran or was down is unread; what was sent to an earlier spawn, or between
// spawns, never reaches a later one, plain messages and migrated mail included. A name
// never spawned (a queen) reads all of its mail. Unread and UnreadCounts agree.
func TestMailIsScopedToTheOpenSpawn(t *testing.T) {
	ctx := context.Background()
	lb := open(t, filepath.Join(t.TempDir(), "bees.db"))
	if _, err := lb.Send(ctx, "queen-toy", "toy-1", "before any spawn"); err != nil {
		t.Fatal(err)
	}
	s := spawn(t, lb, "toy-1", "toy", "w1:p1", "branch")
	if _, err := lb.Send(ctx, "queen-toy", "toy-1", "while it runs"); err != nil {
		t.Fatal(err)
	}
	want := func(step string, bodies ...string) {
		t.Helper()
		ms, err := lb.Unread(ctx, "toy-1")
		var got []string
		for _, m := range ms {
			got = append(got, m.Body)
		}
		counts, cerr := lb.UnreadCounts(ctx)
		if err != nil || cerr != nil || !slices.Equal(got, bodies) || counts["toy-1"] != len(bodies) {
			t.Fatalf("%s: unread %q (count %d), want %q; %v %v", step, got, counts["toy-1"], bodies, err, cerr)
		}
	}
	want("open", "brief for toy-1", "while it runs")
	if err := lb.RecordEnd(ctx, "queen-toy", s.ID, logbook.Ended{}); err != nil {
		t.Fatal(err)
	}
	if _, err := lb.Send(ctx, "queen-toy", "toy-1", "after it ended"); err != nil {
		t.Fatal(err)
	}
	want("ended")
	spawn(t, lb, "toy-1", "toy", "w2:p1", "branch")
	want("respawned", "brief for toy-1")
	if _, err := lb.Send(ctx, "toy-1", "queen-toy", "hi"); err != nil {
		t.Fatal(err)
	}
	if ms, _ := lb.Unread(ctx, "queen-toy"); len(ms) != 1 {
		t.Fatalf("the queen's unread: %+v", ms)
	}
}

// Migrated mail is scoped too: a v0 logbook's unread message to an ended worker, and its
// brief, never reach that worker's next spawn (bees-13.9 review SPEC9).
func TestMigratedMailIsScopedToTheOpenSpawn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bees.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE entries (id TEXT PRIMARY KEY, ts TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('message','received','rang','spawned','ended')),
  actor TEXT NOT NULL, subject TEXT, ref TEXT REFERENCES entries(id),
  data TEXT CHECK (data IS NULL OR json_valid(data))) STRICT;
INSERT INTO entries VALUES ('s1','2026-10-01T00:00:00.000Z','spawned','queen-bees','bees-8',NULL,'{"bead":"bees-8","workspace":"w2","pane":"w2:p1","worktree":"/r/bees-WT/bees-8","branch":"bees-8","repo":"/r/bees"}');
INSERT INTO entries VALUES ('m1','2026-10-01T00:00:01.000Z','message','queen-bees','bees-8','s1','{"body":"old brief"}');
INSERT INTO entries VALUES ('e1','2026-10-01T00:00:02.000Z','ended','queen-bees','bees-8','s1',NULL);
INSERT INTO entries VALUES ('m2','2026-10-01T00:00:03.000Z','message','queen-bees','bees-8',NULL,'{"body":"sent after it ended"}');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	lb := open(t, path) // migrates
	spawn(t, lb, "bees-8", "default", "w3:p1", "branch")
	ms, err := lb.Unread(ctx, "bees-8")
	if err != nil || len(ms) != 1 || ms[0].Body != "brief for bees-8" {
		t.Fatalf("the new spawn's unread after migration: %+v, %v", ms, err)
	}
}
