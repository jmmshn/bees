package scenario_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/herdr"
)

// Identity is ancestry in one project (ADR-0010 Amendment 4, bees-13.69): a worker is
// itself on every command, with its pane hint or without, and runs only what the
// permission table gives a worker; its --as naming anyone else is exit 2.
func TestAWorkerIsItselfWithOrWithoutItsPaneHint(t *testing.T) {
	x := newToy(t)
	x.startQueen()
	x.spawn("toy-1", "verdict")
	x.spawn("toy-2", "verdict")
	n := 0
	run := func(code int, want string, args ...string) {
		t.Helper()
		n++
		x.harness.Step(t, "toy-1", append([]string{"bs"}, args...)...)
		if r := x.harness.Await(t, "toy-1", n); r.Code != code || !strings.Contains(r.Stdout+r.Stderr, want) {
			t.Fatalf("%q: exit %d, want %d with %q:\n%s%s", args, r.Code, code, want, r.Stdout, r.Stderr)
		}
	}
	for _, pane := range []string{"", "HERDR_PANE_ID="} {
		if pane != "" { // env -u HERDR_PANE_ID: no hint, only its ancestry
			n++
			x.harness.Step(t, "toy-1", "env", pane)
			x.harness.Await(t, "toy-1", n)
		}
		run(2, "toy-1 runs this as a worker; only the queen or the keeper ends a worker", "end", "--incomplete", "toy-2")
		run(2, "toy-1 runs this as a worker; only the queen or the keeper prunes", "prune")
		run(2, "toy-1 runs this as a worker; a worker never changes a goal", "goal", "toy-2", "note")
		run(2, "toy-1 runs this as a worker; a worker checks only its own work", "check", "toy-2")
		run(2, "toy-1 runs this as a worker; only the queen or the keeper spawns a worker", "spawn", "--goal", "verdict", "toy-3")
		for _, args := range [][]string{{"done", "--as", "toy-2"}, {"send", "--as", "toy-2", "queen-toy", "hi"}, {"inbox", "--as", "toy-2"}, {"end", "--as", "queen-toy", "toy-2"}} {
			run(2, "--as "+args[2]+": this bs runs under toy-1", args...)
		}
		run(0, "as toy-1 (worker of toy, spawned ", "status")
	}
	// The queen in her pane is the queen, and the keeper alone discards.
	must(t, x.queenRuns(1, "goal", "toy-2", "discard"), 2, "queen-toy runs this as the queen; only the keeper discards a goal")
	must(t, x.queenRuns(2, "status"), 0, "as queen-toy (queen of toy) · logbook "+x.logbook)
	must(t, x.keeper("goal", "toy-2", "discard"), 0, "(discarded)")
	must(t, x.queenRuns(3, "end", "--incomplete", "toy-1"), 0, "ended toy-1")
}

// The tripwire: a pane split into a live worker's herdr workspace, which no entry
// recorded, is that worker, with a warning (contract case ident-tripwire covers a pane
// working in the worker's directory from a herdr workspace of its own).
func TestAPaneSplitIntoAWorkersWorkspaceIsThatWorker(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	sp := x.spawn("toy-2", "verdict")
	pane := x.herdr.Split(x.session, sp.Herdr.Workspace)
	h := herdr.Client{Run: x.herdr, Session: x.session}
	if err := h.StartAgent(context.Background(), pane, "side", "claude", "--name", "side", "--state", x.harness.Dir("side")); err != nil {
		t.Fatal(err)
	}
	must(t, x.worker("side", 1, "end", "--incomplete", "toy-1"), 2, "taken for toy-2", "toy-2 runs this as a worker; only the queen or the keeper ends a worker")
	must(t, x.worker("side", 2, "send", "--as", "keeper", "queen-toy", "hi"), 2, "--as keeper: this bs runs under toy-2")
	if err := h.ClosePane(context.Background(), pane); err != nil {
		t.Fatal(err)
	}
	must(t, x.keeper("end", "--incomplete", "toy-1"), 0, "ended toy-1")
}

// BS_* is the keeper's: under a recorded session each is ignored with a warning, and the
// repo the session runs in decides its project; outside every repo, its own (OQ-K6).
func TestASessionIgnoresBSVariables(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	x.harness.Step(t, "toy-1", "env", "BS_PROJECT=other")
	x.harness.Step(t, "toy-1", "cd", os.TempDir())
	x.harness.Await(t, "toy-1", 2)
	must(t, x.worker("toy-1", 3, "status"), 0, "warning: ignoring BS_PROJECT", "as toy-1 (worker of toy", "· logbook "+x.logbook)
}
