package scenario_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/fake"
	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/logbook"
)

// Queen hosting (bees-13.107, bees-13.109): where a queen's pane is, and where it opens.

// runQueen starts queen-toy as a crossing send does: bs queen typed into the root pane of
// a workspace on the toy's repo. Her harness is that pane's foreground process, so herdr
// finds her pid there. It returns her queen entry once her process runs.
func (x *toy) runQueen() logbook.Queen {
	x.t.Helper()
	h := herdr.Client{Run: x.herdr, Session: x.session}
	w, err := h.CreateWorkspace(context.Background(), x.repo, "queen toy")
	if err != nil {
		x.t.Fatal(err)
	}
	if err := h.RunInPane(context.Background(), w.Pane, "bs queen"); err != nil {
		x.t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if q, ok, _ := x.open().LatestQueen(context.Background(), "queen-toy"); ok {
			if up, _ := x.herdr.Alive(q.Process); up {
				return q
			}
		}
	}
	x.t.Fatal("queen-toy never came up")
	return logbook.Queen{}
}

// bees-13.109: the keeper moves the queen's pane (herdr pane move gives it a new id). A
// worker's send rings her in the pane she is in now, which a ring typed into her pane
// (agy's) needs; bs records her there again, same process, and status shows the new pane.
func TestAMovedQueenIsRungInHerNewPane(t *testing.T) {
	x, _ := agyToy(t)
	must(t, x.worker("toy-1", 1, "inbox"), 0) // its brief, read: only the queen's pane is in question
	q := x.runQueen()
	x.herdr.SetScreen(x.session, q.Pane, fake.Screen{Status: herdr.Idle})
	x.move(q.Pane, "")
	r := x.keeper("status")
	must(t, r, 0)
	moved, _, err := x.open().LatestQueen(context.Background(), "queen-toy")
	if err != nil || moved.Pane == q.Pane || moved.MovedFrom != q.Pane || moved.Process != q.Process {
		t.Fatalf("after the move, her entry is %+v (%v); was %+v", moved, err, q)
	}
	if got := cell(r.out, "queen-toy", "WORKSPACE"); !strings.Contains(r.out, "pane "+moved.Pane+" on herdr session") {
		t.Fatalf("status shows %q, want pane %s:\n%s", got, moved.Pane, r)
	}
	must(t, x.worker("toy-1", 2, "send", "queen-toy", "moved?"), 0, "rung queen-toy")
	if p := x.herdr.Prompts(x.session, moved.Pane); len(p) != 1 || !strings.HasPrefix(p[0], "[bs] 1 unread (toy-1)") {
		t.Fatalf("typed into her new pane: %q", p)
	}
	if n := x.sqlOne(`SELECT count(*) FROM entries WHERE kind = 'queen'`); n != "2" {
		t.Fatalf("%s queen entries, want 2: the start and one move", n)
	}
}

// A ring finds the moved queen without a status first, and records her move once.
func TestARingFollowsAMovedQueen(t *testing.T) {
	x, _ := agyToy(t)
	must(t, x.worker("toy-1", 1, "inbox"), 0) // its brief, read: only the queen's pane is in question
	q := x.runQueen()
	x.herdr.SetScreen(x.session, q.Pane, fake.Screen{Status: herdr.Idle})
	x.move(q.Pane, "")
	must(t, x.worker("toy-1", 2, "send", "queen-toy", "moved?"), 0, "rung queen-toy")
	moved, _, _ := x.open().LatestQueen(context.Background(), "queen-toy")
	if p := x.herdr.Prompts(x.session, moved.Pane); moved.Pane == q.Pane || len(p) != 1 {
		t.Fatalf("her entry %+v; typed into her new pane: %q", moved, p)
	}
	must(t, x.keeper("status"), 0, "pane "+moved.Pane+" on herdr session")
	if n := x.sqlOne(`SELECT count(*) FROM entries WHERE kind = 'queen'`); n != "2" {
		t.Fatalf("%s queen entries, want 2: the start and one move", n)
	}
}

// bees-13.107: a crossing send that starts a queen opens her in a tab of her repo's
// primary herdr workspace when the keeper has one open, so herdr nests her under the repo
// beside her workers; with none, in a workspace of her own, as before.
func TestACrossingSendStartsTheQueenInHerReposWorkspace(t *testing.T) {
	x := newToy(t)
	ctx := context.Background()
	repo, db := x.otherProject()
	primary := x.herdr.OpenRepo(x.session, repo, "other")
	x.spawn("toy-1", "verdict")
	x.harness.Step(t, "toy-1", "cd", repo)
	x.harness.Await(t, "toy-1", 1)
	must(t, x.worker("toy-1", 2, "send", "queen-other", "hi"), 0, "started queen-other in pane "+primary+":p2", "rung queen-other")
	ro, err := logbook.OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if q, ok, err := ro.LatestQueen(ctx, "queen-other"); err != nil || !ok || q.Pane != primary+":p2" {
		t.Fatalf("queen-other's entry: %+v %v %v, want pane %s:p2", q, ok, err, primary)
	}
}
