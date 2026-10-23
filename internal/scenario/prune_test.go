package scenario_test

import (
	"context"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/fake"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
)

// A tag named like the base, on the branch's tip, proves nothing: the base is the branch
// refs/heads/main, never whatever "main" resolves to first (motif 3).
func TestATagNamedLikeTheBaseProvesNothing(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.git(x.repo, "tag", "main", "toy-1")
	must(t, x.queen("end", "toy-1"), 9, "toy-1 has work not on main")
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir+" (toy-1): toy-1 has work not on main")
}

// A base whose upstream is gone (fetch --prune after its PR merged) is measured against
// the base alone: never "can't check" forever (motif 5).
func TestAGoneUpstreamIsSkipped(t *testing.T) {
	x := newToy(t)
	x.git(x.repo, "config", "branch.main.remote", "origin")
	x.git(x.repo, "config", "branch.main.merge", "refs/heads/main")
	x.git(x.repo, "remote", "add", "origin", "/nonexistent/origin.git")
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	must(t, x.queen("end", "toy-1"), 9, "toy-1 has work not on main")
	x.git(x.repo, "merge", "-q", "toy-1")
	must(t, x.queen("end", "toy-1"), 0, "ended toy-1")
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir+" and branch toy-1")
}

// Commits made on a detached HEAD are on no branch: prune keeps the worktree that holds
// them, saying how to keep them.
func TestPruneKeepsCommitsOnADetachedHead(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "verdict")
	x.git(sp.Workspace.Dir, "checkout", "-q", "--detach")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done", "--answer", "yes", "--report", "fine"), 0)
	must(t, x.queen("end", "toy-1"), 0)
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir+" (toy-1): its HEAD has commits on no branch", "switch -c")
}

// A herdr pane working in an ended worker's directory (a keeper's shell, a pane moved
// out) keeps it: removing it would pull the floor from under that pane (the pilot's
// freeWorktree).
func TestPruneKeepsADirAPaneWorksIn(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.git(x.repo, "merge", "-q", "toy-1")
	must(t, x.queen("end", "toy-1"), 0)
	out, err := x.herdr.Run(context.Background(), "", "herdr", "--session", "toy", "workspace", "create", "--cwd", sp.Workspace.Dir, "--label", "keeper")
	if err != nil {
		t.Fatalf("workspace create: %v %s", err, out)
	}
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir+" (toy-1): herdr pane w2:p1 works in it")
}

// An ended worker whose recorded process still runs may still write in its directory:
// prune keeps it until that process stops.
func TestPruneKeepsAnEndedWorkerThatStillRuns(t *testing.T) {
	x := newToy(t)
	ctx := context.Background()
	x.herdr.Programs["claude"] = "/bin/true" // the start is never confirmed
	x.ports.Timing.Confirm = 300 * time.Millisecond
	x.bead("toy-1", "x")
	must(t, x.queen("spawn", "--label", "x", "--goal", "verdict", "toy-1"), 8)
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	// It started after all, late: a process bs recorded and couldn't stop.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	start, err := proc.Start(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	lb, err := logbook.Open(ctx, x.logbook)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	sp, _, err := lb.LatestSpawn(ctx, "toy-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := lb.RecordStartOnce(ctx, logbook.Start{Spawn: sp.ID, Name: sp.Name, Process: proc.Process{PID: cmd.Process.Pid, Start: start}, Harness: "claude"}); err != nil {
		t.Fatal(err)
	}
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir+" (toy-1): its ended worker still runs (pid "+strconv.Itoa(cmd.Process.Pid)+")")
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait() // killed: its error is the signal
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir+" and branch toy-1")
}

// A spawn ended --incomplete with nothing handed off can never complete: status points
// at the keeper's discard, not at a prune that would never complete it.
func TestStatusPointsAnUnhandedGoalAtDiscard(t *testing.T) {
	x := newToy(t)
	x.herdr.Programs["claude"] = "/bin/true"
	x.ports.Timing.Confirm = 300 * time.Millisecond
	x.bead("toy-1", "x")
	must(t, x.queen("spawn", "--label", "x", "--goal", "branch", "toy-1"), 8)
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	x.herdr.Programs["claude"] = fake.Beesfake(t)
	must(t, x.queen("prune"), 0)
	must(t, x.keeper("status"), 0, "stuck: toy-1: ended with nothing handed off, so its goal can't complete → the keeper discards it (bs goal toy-1 discard, bees-13.6)")
}

// An origin that isn't on github.com (an ssh alias, GitHub Enterprise) is never asked for
// merged pull requests, and the reason says so rather than only "has work not on main".
func TestANonGitHubOriginSaysNoPullRequestWasLookedFor(t *testing.T) {
	x := newToy(t)
	x.git(x.repo, "remote", "add", "origin", "git@gh-work:x/toy.git")
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	must(t, x.queen("end", "toy-1"), 9, "toy-1 has work not on main (origin git@gh-work:x/toy.git is not on github.com, so no pull request was looked for)")
}

// A worktree git refuses to remove (locked) is kept, naming the way out (exit 3), never
// "couldn't check" (exit 4) on every prune.
func TestPruneKeepsALockedWorktree(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.git(x.repo, "merge", "-q", "toy-1")
	must(t, x.queen("end", "toy-1"), 0)
	x.git(x.repo, "worktree", "lock", sp.Workspace.Dir)
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir+" (toy-1): git refused to remove it", "git worktree unlock")
	x.git(x.repo, "worktree", "unlock", sp.Workspace.Dir)
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir+" and branch toy-1")
}
