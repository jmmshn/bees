package scenario_test

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/proc"
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
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir+" and branch toy-1 (toy-1)")
	if _, err := os.Stat(sp.Workspace.Dir); !os.IsNotExist(err) {
		t.Fatalf("prune kept %s", sp.Workspace.Dir)
	}
	if x.hasBranch("toy-1") {
		t.Fatal("prune kept the merged branch toy-1")
	}
	x.spawn("toy-1", "branch") // the bead's next worker starts clean
}

// bs end's next: keeps the flags it was given, so --incomplete and --force never send the
// queen back and forth: the way past both refusals is printed whole (bees-13.52).
func TestEndNextKeepsItsFlags(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "branch")
	x.harness.Step(t, "toy-1", "child", "server")
	x.harness.Await(t, "toy-1", 1)
	must(t, x.queen("end", "--incomplete", "toy-1"), 5, "background work", "next: bs end --incomplete --force toy-1")
	must(t, x.queen("end", "--force", "toy-1"), 9, "next: bs end --incomplete --force toy-1")
	must(t, x.queen("end", "--incomplete", "--force", "toy-1"), 0, "ended toy-1")
}

// The background-work gate on real processes: a child the start didn't see refuses bs
// done (exit 5) and names it; --force hands off anyway and records the gate as waived,
// never passed; a killed child clears it; a detached one escapes, still running (the
// speed bump ADR-0010 accepts).
func TestTheGateRefusesBackgroundWork(t *testing.T) {
	x := newToy(t)
	ctx := context.Background()
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	x.harness.Step(t, "toy-1", "child", "server")
	kid := x.harness.Await(t, "toy-1", 1).PID
	st, _, _ := x.open().LatestStart(ctx, sp.ID)
	if ds, _ := x.herdr.Descendants(st.PID); !slices.ContainsFunc(ds, func(p proc.Process) bool { return p.PID == kid }) {
		t.Fatalf("the child %d isn't running under the harness: %+v", kid, ds)
	}
	r := x.worker("toy-1", 2, "done")
	must(t, r, 5, "background work")
	if !strings.Contains(r.String(), "pid "+strconv.Itoa(kid)) {
		t.Fatalf("gate doesn't name pid %d:\n%s", kid, r)
	}
	must(t, x.worker("toy-1", 3, "done", "--force"), 0, "handed off")
	d, _, _ := x.open().LatestDone(ctx, sp.ID)
	if c := d.Checks[0]; c.Name != goal.NoBackground || c.Pass || !c.Waived ||
		!slices.ContainsFunc(d.Background, func(b proc.Background) bool { return b.PID == kid }) {
		t.Fatalf("a forced done records %+v, background %+v; want no_background waived, not passed, naming %d", c, d.Background, kid)
	}
	x.harness.Step(t, "toy-1", "kill", "server")
	x.harness.Await(t, "toy-1", 4)
	x.harness.Step(t, "toy-1", "detach", "escapee")
	esc := x.harness.Await(t, "toy-1", 5).PID
	if _, err := proc.Start(esc); err != nil {
		t.Fatalf("the detached child %d isn't running: %v", esc, err)
	}
	must(t, x.worker("toy-1", 6, "done"), 0, "handed off")
	if d, _, _ := x.open().LatestDone(ctx, sp.ID); !d.Checks[0].Pass {
		t.Fatalf("a clean done records %+v", d.Checks[0])
	}
}

// --force waives the gate and nothing else: an unmet handoff check still refuses.
func TestDoneForceWaivesOnlyTheGate(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "branch")
	x.harness.Step(t, "toy-1", "child", "server")
	x.harness.Await(t, "toy-1", 1)
	must(t, x.worker("toy-1", 2, "done", "--force"), 7, goal.BranchAhead)
}

// bs done for a worker with no open spawn, unknown or ended, is a refusal that says
// where to look, never a database error.
func TestDoneForNoOpenSpawnIsRefused(t *testing.T) {
	x := newToy(t)
	x.start()
	must(t, x.keeper("done", "--as", "toy-7"), 2, "no open spawn for toy-7", "next: bs status")
	x.spawn("toy-1", "verdict")
	must(t, x.worker("toy-1", 1, "done", "--answer", "yes", "--report", "ok"), 0)
	must(t, x.queen("end", "toy-1"), 0)
	must(t, x.keeper("done", "--as", "toy-1"), 2, "no open spawn for toy-1")
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
