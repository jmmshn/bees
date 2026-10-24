package scenario_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/logbook"
)

// Setup and visibility (bees-13.8): status's lines, status with herdr down, and mail
// across projects.

// startQueenOf runs bs queen in a new keeper's shell pane on dir, a repo of project, and
// waits for her harness.
func (x *toy) startQueenOf(project, dir string) {
	x.t.Helper()
	shell := "keeper-" + project
	x.shell(x.session, shell, dir)
	x.harness.Step(x.t, shell, "bs&", "queen")
	x.harness.Await(x.t, shell, 1)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(x.harness.Dir("queen-"+project), "ring.sock")); err == nil {
			return
		}
	}
	x.t.Fatalf("queen-%s's harness never came up", project)
}

// Status says, with the next command, what the table alone leaves open: a waived check,
// a discarded live goal, a complete goal whose workspace is still on disk.
func TestStatusNamesWaivedDiscardedAndKept(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "branch")
	must(t, x.queen("goal", "toy-1", "--waive", goal.BranchAhead), 0)
	x.spawn("toy-2", "branch")
	must(t, x.keeper("goal", "toy-2", "discard"), 0)
	sp := x.spawn("toy-3", "verdict")
	must(t, x.worker("toy-3", 1, "done", "--answer", "yes", "--report", "fine"), 0)
	must(t, x.queen("end", "toy-3"), 0)
	x.write(filepath.Join(sp.Workspace.Dir, "scratch.txt"), "unsaved")
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir+" (toy-3)")
	r := x.keeper("status")
	must(t, r, 0,
		"waived: toy-1: branch_ahead no longer checked → bs goal toy-1 branch puts it back",
		"discarded: toy-2: the keeper discarded its goal: the work is wanted nowhere → bs end toy-2",
		"kept: toy-3: its goal is complete, but its workspace is still on disk → bs prune removes it, or names what keeps it")
	if strings.Contains(r.out, "stuck:") {
		t.Fatalf("nothing is stuck:\n%s", r)
	}
	if err := os.Remove(filepath.Join(sp.Workspace.Dir, "scratch.txt")); err != nil {
		t.Fatal(err)
	}
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir)
	// Ended, removed, nothing to do: history, which only --all lists (bees-13.78.17).
	if r := x.keeper("status"); lineOf(r.out, "toy-3 ") != "" || !strings.Contains(r.out, "1 ended workers with their workspace gone and nothing to do are hidden → bs status --all lists them") {
		t.Fatalf("after prune:\n%s", r)
	}
	if r := x.keeper("status", "--all"); cell(r.out, "toy-3", "WORKSPACE") != "toy-3 (removed)" || strings.Contains(r.out, "kept: toy-3") || strings.Contains(r.out, "hidden") {
		t.Fatalf("after prune, --all:\n%s", r)
	}
	must(t, x.queen("end", "toy-2"), 0)
	if r := x.keeper("status"); strings.Contains(r.out, "discarded: toy-2") || !strings.Contains(r.out, "kept: toy-2: its goal is discarded") {
		t.Fatalf("after bs end toy-2:\n%s", r)
	}
}

// Status sorts workers by name with numbers as numbers, and shows a workspace relative to
// paths.worktrees, naming that dir once (bees-13.78.17).
func TestStatusSortsNaturallyWithRelativeWorkspaces(t *testing.T) {
	x := newToy(t)
	for _, n := range []string{"toy-10", "toy-2", "toy-1"} {
		x.spawn(n, "verdict")
	}
	r := x.keeper("status")
	must(t, r, 0, "workspaces are under "+filepath.Join(filepath.Dir(x.repo), "toy-WT"))
	i1, i2, i10 := strings.Index(r.out, "\ntoy-1 "), strings.Index(r.out, "\ntoy-2 "), strings.Index(r.out, "\ntoy-10 ")
	if i1 < 0 || i1 > i2 || i2 > i10 || cell(r.out, "toy-10", "WORKSPACE") != "toy-10" {
		t.Fatalf("not toy-1, toy-2, toy-10 with relative workspaces:\n%s", r)
	}
}

// T5: status reads the record and the process table, never herdr: with the server down it
// still lists every worker and whether it runs.
func TestStatusWorksWithHerdrDown(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	x.herdr.Down = true
	r := x.keeper("status")
	must(t, r, 0)
	if cell(r.out, "toy-1", "RUNNING") != "yes" {
		t.Fatalf("status with herdr down:\n%s", r)
	}
	x.bead("toy-2", "work on toy-2")
	must(t, x.queen("spawn", "--label", "x", "--goal", "verdict", "toy-2"), 1, "nothing was recorded", "server_not_running")
}

// Status shows each worker's label and every session's unread mail (bees-13.73.1): a
// brief nobody read, and mail for a session no spawn names, the queen included.
func TestStatusShowsLabelAndUnread(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	r := x.keeper("status")
	if cell(r.out, "toy-1", "LABEL") != "toy 1" || cell(r.out, "toy-1", "UNREAD") != "1" {
		t.Fatalf("an unread brief:\n%s", r)
	}
	must(t, x.worker("toy-1", 1, "inbox"), 0)
	must(t, x.worker("toy-1", 2, "send", "queen-toy", "a question"), 0)
	r = x.keeper("status")
	if cell(r.out, "toy-1", "UNREAD") != "0" || cell(r.out, "queen-toy", "UNREAD") != "1" || cell(r.out, "queen-toy", "STATE") != "no spawn" {
		t.Fatalf("after the brief is read and the queen is mailed:\n%s", r)
	}
}

// T11 between two queens on one herdr server: a send with --project writes only the
// recipient's logbook, keeping the sender and its project; the doorbell and bs inbox give
// the reply command with --project; the reply routes back. Inside a pane, inbox and done
// refuse another project, and --as can't borrow another session's name.
func TestMailAcrossProjects(t *testing.T) {
	x := newToy(t)
	ctx := context.Background()
	other := filepath.Join(filepath.Dir(x.repo), "other")
	x.git(filepath.Dir(x.repo), "init", "-q", "-b", "main", other)
	x.startQueen()
	x.startQueenOf("other", other)

	must(t, x.queenRuns(1, "send", "--project", "other", "queen-other", "hi from toy"), 0, "rung queen-other")
	if rings := x.harness.Rings("queen-other"); len(rings) != 1 || !strings.Contains(rings[0], `1 unread (queen-toy) — run: bs inbox   (reply with: bs send --project toy queen-toy "...")`) {
		t.Fatalf("queen-other's rings: %q", rings)
	}
	path := filepath.Join(filepath.Dir(x.logbook), "other.db")
	lb, err := logbook.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := lb.Unread(ctx, "queen-other")
	lb.Close()
	if err != nil || len(ms) != 1 || ms[0].From != "queen-toy" || ms[0].FromProject != "toy" {
		t.Fatalf("other.db: %+v %v", ms, err)
	}
	if ms, _ := x.open().Unread(ctx, "queen-other"); len(ms) != 0 {
		t.Fatalf("toy.db got a copy: %+v", ms)
	}
	must(t, x.worker("queen-other", 1, "inbox"), 0, "from queen-toy", "hi from toy", `(reply with: bs send --project toy queen-toy "...")`)
	must(t, x.worker("queen-other", 2, "send", "--project", "toy", "queen-toy", "hello back"), 0, "rung queen-toy")
	must(t, x.queenRuns(2, "inbox"), 0, "from queen-other", "hello back", `(reply with: bs send --project other queen-other "...")`)

	must(t, x.queenRuns(3, "inbox", "--project", "other"), 2, "flag provided but not defined: -project")
	must(t, x.queenRuns(4, "send", "--as", "queen-other", "--project", "other", "queen-other", "spoof"), 2, "--as queen-other: this bs runs under queen-toy")
	x.bead("toy-1", "work on toy-1")
	must(t, x.queenRuns(5, "spawn", "--label", "x", "--goal", "verdict", "toy-1"), 0)
	must(t, x.worker("toy-1", 1, "done", "--project", "other", "--answer", "yes", "--report", "ok"), 2, "flag provided but not defined: -project")
	must(t, x.keeper("inbox", "--as", "queen-toy", "--project", "other"), 2, "flag provided but not defined: -project")
}
