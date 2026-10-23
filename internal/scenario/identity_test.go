package scenario_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/herdr"
)

// Identity is ancestry (ADR-0010, bees-13.49, bees-13.50): a worker is itself on every
// command, with its pane env or without, and does only its own done, check, send and inbox;
// its --as naming anyone else is exit 2. The keeper's --as X acts as X, with X's
// permissions, and an inbox read so only peeks. An unrecorded pane in a live worker's
// workspace is that worker. Only the keeper discards; the queen and the keeper end and
// prune.
func TestIdentityIsAncestry(t *testing.T) {
	x := newToy(t)
	x.startQueen()
	x.spawn("toy-1", "verdict")
	sp2 := x.spawn("toy-2", "verdict")
	n := 0
	for _, step := range []string{"bs", "bs-nopane"} {
		run := func(code int, want string, args ...string) {
			t.Helper()
			n++
			x.harness.Step(t, "toy-1", append([]string{step}, args...)...)
			r := x.harness.Await(t, "toy-1", n)
			if r.Code != code || !strings.Contains(r.Stdout+r.Stderr, want) {
				t.Fatalf("%s %q: exit %d, want %d with %q:\n%s%s", step, args, r.Code, code, want, r.Stdout, r.Stderr)
			}
		}
		run(2, "toy-1 runs this as a worker; only the queen or the keeper ends a worker", "end", "--incomplete", "toy-2")
		run(2, "toy-1 runs this as a worker; only the queen or the keeper prunes", "prune")
		run(2, "toy-1 runs this as a worker; only the queen or the keeper changes a goal", "goal", "toy-2", "note")
		run(2, "toy-1 runs this as a worker; only the queen or the keeper checks another worker", "check", "toy-2")
		run(2, "toy-1 runs this as a worker; only the queen or the keeper spawns a worker", "spawn", "--goal", "verdict", "toy-3")
		for _, args := range [][]string{{"done", "--as", "toy-2"}, {"send", "--as", "toy-2", "queen-toy", "hi"}, {"inbox", "--as", "toy-2"}, {"end", "--as", "queen-toy", "toy-2"}} {
			run(2, "--as "+args[2]+": this bs runs under toy-1", args...)
		}
		run(0, "", "inbox")
	}

	// The keeper's --as X is X, with X's permissions; its inbox --as only peeks.
	must(t, x.keeper("goal", "--as", "toy-2", "toy-2", "note"), 2, "toy-2 runs this as a worker; only the queen or the keeper changes a goal")
	must(t, x.keeper("goal", "--as", "queen-toy", "toy-2", "discard"), 2, "queen-toy runs this as the queen; only the keeper discards a goal")
	must(t, x.keeper("end", "--as", "toy-1", "--incomplete", "toy-2"), 2, "toy-1 runs this as a worker")
	must(t, x.keeper("send", "toy-2", "peek at me"), 0, "sent")
	must(t, x.keeper("inbox", "--as", "toy-2"), 0, "peek at me", "(peeked as toy-2 with --as: nothing marked read)")
	must(t, x.worker("toy-2", 1, "inbox"), 0, "from keeper", "peek at me")

	// An unrecorded pane in toy-2's workspace is toy-2, with a warning.
	pane := x.herdr.Split(x.session, sp2.Herdr.Workspace)
	h := herdr.Client{Run: x.herdr, Session: x.session}
	if err := h.StartAgent(context.Background(), pane, "side", "claude", "--name", "side", "--state", x.harness.Dir("side")); err != nil {
		t.Fatal(err)
	}
	must(t, x.worker("side", 1, "end", "--incomplete", "toy-1"), 2, "taken for toy-2", "toy-2 runs this as a worker; only the queen or the keeper ends a worker")
	must(t, x.worker("side", 2, "send", "--as", "keeper", "queen-toy", "hi"), 2, "--as keeper: this bs runs under toy-2")
	if err := h.ClosePane(context.Background(), pane); err != nil {
		t.Fatal(err)
	}

	// Only the keeper discards; the queen and the keeper end and prune.
	must(t, x.keeper("goal", "toy-2", "discard"), 0, "(discarded)")
	must(t, x.worker("toy-2", 2, "inbox"), 0, "keeper discarded your goal")
	must(t, x.queenRuns(1, "end", "--incomplete", "toy-1"), 0, "ended toy-1")
	must(t, x.keeper("end", "toy-2"), 0, "ended toy-2")
	must(t, x.queenRuns(2, "prune"), 0)
	must(t, x.keeper("prune"), 0)
}
