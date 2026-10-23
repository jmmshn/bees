package scenario_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jmmshn/bees/internal/goal"
)

// bs goal: the keeper or the queen amends a live goal (re-running the spawn refusals) or
// waives one check, and the worker is told; a worker never changes a goal; only the
// keeper discards, and only a discard changes an ended goal.
func TestBsGoalAmendWaiveDiscard(t *testing.T) {
	x := newToy(t)
	ctx := context.Background()
	x.startQueen()
	sp := x.spawn("toy-1", "branch")
	must(t, x.keeper("goal", "toy-1", "verdict"), 0, "toy-1: --goal verdict")
	must(t, x.worker("toy-1", 1, "inbox"), 0, "keeper changed your goal: it is now --goal verdict")
	must(t, x.keeper("goal", "toy-1", "pr"), 2, "belongs to no github repo") // the spawn refusals, again
	must(t, x.keeper("goal", "toy-1", "--waive", goal.BranchAhead), 2, "runs no such check")
	must(t, x.queenRuns(1, "goal", "toy-1", "branch"), 0, "--goal branch")
	must(t, x.keeper("goal", "toy-1", "--waive", goal.NoBackground), 2, "--force")
	must(t, x.queenRuns(2, "goal", "toy-1", "--waive", goal.BranchAhead), 0, "(waived: branch_ahead)")
	must(t, x.worker("toy-1", 2, "done"), 0) // nothing committed: branch_ahead is waived
	must(t, x.worker("toy-1", 3, "goal", "toy-1", "branch:ready"), 2, "a worker never changes a goal")
	must(t, x.queenRuns(3, "goal", "toy-1", "discard"), 2, "only the keeper discards")
	if e, _ := x.open().Goal(ctx, sp); e.String() != "branch" || len(e.Waived) != 1 || e.Discarded {
		t.Fatalf("effective goal %+v", e)
	}
	x.commit(sp.Workspace.Dir, "late.txt") // work not on main: the goal stays incomplete
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	must(t, x.keeper("goal", "toy-1", "verdict"), 2, "it has ended")
	must(t, x.keeper("goal", "toy-1", "discard"), 0, "(discarded)")
	must(t, x.keeper("status"), 0, "--goal branch (discarded)")

	// After landed: nothing left to amend.
	x.spawn("toy-2", "verdict")
	must(t, x.worker("toy-2", 1, "done", "--answer", "yes", "--report", "ok"), 0)
	must(t, x.keeper("goal", "toy-2", "note"), 2, "complete (landed)")
}

// bs check runs the handoff checks now, prints each, and writes nothing.
func TestBsCheckWritesNothing(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.write(filepath.Join(sp.Workspace.Dir, "notes.txt"), "draft")
	before := entries(t, x)
	r := x.keeper("check", "toy-1")
	must(t, r, 7, "ok   no_background", "FAIL branch_ahead", "FAIL clean: uncommitted or untracked files", "notes.txt")
	if after := entries(t, x); after != before {
		t.Fatalf("bs check wrote %d entries", after-before)
	}
	x.commit(sp.Workspace.Dir, "notes.txt")
	must(t, x.keeper("check", "toy-1"), 0, "ok   branch_ahead: 1 commit ahead of main")
	must(t, x.keeper("check", "toy-9"), 2, "no worker toy-9")
}

func entries(t *testing.T, x *toy) int {
	t.Helper()
	db, err := sql.Open("sqlite", x.logbook+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM entries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Only the queen or the keeper ends a worker, prunes, or names itself with --as: a worker
// ends no sibling and prunes nothing (bees-13.49), --as names no worker, and a discard is
// always the keeper's, whatever --as says (bees-13.50).
func TestWorkerNeverEndsPrunesOrPosesAsQueen(t *testing.T) {
	x := newToy(t)
	x.startQueen()
	x.spawn("toy-1", "verdict")
	x.spawn("toy-2", "verdict")
	must(t, x.worker("toy-1", 1, "end", "--incomplete", "toy-2"), 2, "run by worker toy-1", "a worker never ends a worker")
	must(t, x.worker("toy-1", 2, "prune"), 2, "run by worker toy-1", "a worker never prunes")
	must(t, x.keeper("end", "--as", "toy-1", "--incomplete", "toy-2"), 2, "--as toy-1: a worker never ends a worker")
	must(t, x.keeper("prune", "--as", "toy-1"), 2, "--as toy-1: a worker never prunes")
	must(t, x.keeper("goal", "--as", "toy-2", "toy-2", "note"), 2, "--as toy-2: only the queen")
	must(t, x.keeper("goal", "--as", "queen-toy", "toy-2", "discard"), 0, "(discarded)")
	must(t, x.worker("toy-2", 1, "inbox"), 0, "keeper discarded your goal")

	// The queen, in its pane or by --as, and the keeper still end and prune.
	must(t, x.queenRuns(1, "end", "--incomplete", "toy-1"), 0, "ended toy-1")
	must(t, x.queen("end", "toy-2"), 0, "ended toy-2")
	must(t, x.queenRuns(2, "prune"), 0)
	must(t, x.keeper("prune", "--as", "keeper"), 0)
}
