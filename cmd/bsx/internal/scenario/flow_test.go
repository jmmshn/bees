package scenario_test

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jmmshn/bees/cmd/bsx/internal/goal"
	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

// The whole flow on fakes, --goal branch: spawn, a worker's handoff from its pane, the
// completion gate refusing an unmerged branch, end after the merge, prune.
func TestSpawnHandoffEndPruneOnABranchGoal(t *testing.T) {
	x := newToy(t)
	ctx := context.Background()
	sp := x.spawn("toy-1", "branch")
	if sp.Goal.String() != "branch" || sp.Herdr.Session != "toy" || sp.Workspace.Kind != "git_worktree" || sp.Workspace.Base != "main" || sp.ConfigDir != x.config {
		t.Fatalf("spawned = %+v", sp)
	}
	st, ok, err := x.open().LatestStart(ctx, sp.ID)
	if err != nil || !ok || st.PID == 0 {
		t.Fatalf("no confirmed start: %+v, %v, %v", st, ok, err)
	}
	if r := x.worker("toy-1", 1, "inbox"); r.code != 0 || !strings.Contains(r.out, "You own toy-1") || !strings.Contains(r.out, "--goal branch") {
		t.Fatalf("worker's inbox: %s", r)
	}
	// Nothing committed yet: the handoff names the failed check (exit 7).
	must(t, x.worker("toy-1", 2, "done"), 7, goal.BranchAhead)
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 3, "done"), 0, "handed off to queen-toy")
	must(t, x.queen("inbox"), 0, "done: toy-1")
	// Handed off but not merged: bs end is the completion gate (exit 9).
	must(t, x.queen("end", "toy-1"), 9, "not complete", goal.BranchMerged)
	x.git(x.repo, "merge", "-q", "--ff-only", "toy-1")
	must(t, x.queen("end", "toy-1"), 0, "ended toy-1")
	lb := x.open()
	if ld, ok, _ := lb.LandedOf(ctx, sp.ID); !ok || ld.By != "end" {
		t.Fatalf("landed = %+v, %v", ld, ok)
	}
	if ok, _ := x.herdr.Alive(st.Process); ok {
		t.Fatal("bs end left the worker's process running")
	}
	if _, err := os.Stat(sp.Workspace.Dir); err != nil {
		t.Fatalf("bs end removed the workspace: %v", err)
	}
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir)
	if _, err := os.Stat(sp.Workspace.Dir); !os.IsNotExist(err) {
		t.Fatalf("prune kept %s", sp.Workspace.Dir)
	}
}

// The background-work gate on real processes: a child the start didn't see refuses bs
// done (exit 5) and names it; --force hands off anyway and records it; a killed child
// clears it; a detached one escapes (the speed bump ADR-0010 accepts).
func TestTheGateRefusesBackgroundWork(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	x.harness.Step(t, "toy-1", "child", "server")
	kid := x.harness.Await(t, "toy-1", 1).PID
	st, _, _ := x.open().LatestStart(context.Background(), sp.ID)
	if ds, _ := x.herdr.Descendants(st.PID); !slices.ContainsFunc(ds, func(p proc.Process) bool { return p.PID == kid }) {
		t.Fatalf("the child %d isn't running under the harness: %+v", kid, ds)
	}
	r := x.worker("toy-1", 2, "done")
	must(t, r, 5, "background work")
	if !strings.Contains(r.String(), "pid "+strconv.Itoa(kid)) {
		t.Fatalf("gate doesn't name pid %d:\n%s", kid, r)
	}
	x.harness.Step(t, "toy-1", "kill", "server")
	x.harness.Await(t, "toy-1", 3)
	x.harness.Step(t, "toy-1", "detach", "escapee")
	x.harness.Await(t, "toy-1", 4)
	must(t, x.worker("toy-1", 5, "done"), 0, "handed off")
}

// --force waives the gate and nothing else, and the done records what ran.
func TestDoneForceWaivesOnlyTheGate(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "branch")
	x.harness.Step(t, "toy-1", "child", "server")
	x.harness.Await(t, "toy-1", 1)
	must(t, x.worker("toy-1", 2, "done", "--force"), 7, goal.BranchAhead) // still unmet
}

// A verdict goal completes at handoff: done lands it, and end needs no merge.
func TestAVerdictCompletesAtHandoff(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "verdict")
	must(t, x.worker("toy-1", 1, "done", "--answer", "maybe"), 7, "--answer must be yes or no")
	must(t, x.worker("toy-1", 2, "done", "--answer", "yes", "--report", "it holds"), 0, "answer yes")
	if ld, ok, _ := x.open().LandedOf(context.Background(), sp.ID); !ok || ld.By != "done" {
		t.Fatalf("landed = %+v, %v", ld, ok)
	}
	must(t, x.queen("end", "toy-1"), 0)
}

// Rule G: bs done with a positional message is refused and says how (bees-12.51).
func TestDoneRefusesAPositionalReport(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	must(t, x.worker("toy-1", 1, "done", "all good"), 2, "--report")
}
