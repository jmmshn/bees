package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/worker"
)

// endedWorker records an ended spawn of name whose branch lives in repo.
func endedWorker(t *testing.T, name, repo string) {
	t.Helper()
	ctx := context.Background()
	lb, err := logbook.Open(ctx, os.Getenv("BS_LOGBOOK"))
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	id, err := lb.RecordSpawn(ctx, "queen-test", logbook.Spawn{Name: name, Bead: name, Branch: name, Repo: repo}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := lb.RecordEnd(ctx, "queen-test", id); err != nil {
		t.Fatal(err)
	}
}

// repoWithBranch makes a git repo whose default branch is main and whose branch has a
// commit not on it.
func repoWithBranch(t *testing.T, main, branch string) string {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", main)
	git("commit", "-q", "--allow-empty", "-m", "init")
	git("checkout", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "a.txt")
	git("commit", "-q", "-m", "work")
	git("checkout", "-q", main)
	return repo
}

func TestPruneSaysWhatItKeptWithDistinctExitCodes(t *testing.T) {
	useTempLogbook(t)
	saved := newFleet
	t.Cleanup(func() { newFleet = saved })
	newFleet = func(f worker.Fleet) worker.Fleet { return f } // no gh

	if out, _, code := bs(t, "prune"); code != 0 || out != "nothing to prune: no ended worker has a branch left\n" {
		t.Fatalf("empty logbook: exit %d, %q", code, out)
	}
	if _, _, code := bs(t, "prune", "extra"); code != 2 {
		t.Fatalf("prune extra: exit %d, want 2", code)
	}
	endedWorker(t, "test-1", repoWithBranch(t, "main", "test-1"))
	out, _, code := bs(t, "prune")
	if code != exitKeptUnmerged || out != "kept test-1: not merged: it has work not on main\n" {
		t.Fatalf("unmerged: exit %d, %q", code, out)
	}
	endedWorker(t, "test-2", repoWithBranch(t, "trunk", "test-2")) // no main: can't check
	out, _, code = bs(t, "prune", "--dry-run")
	if code != exitKeptUnknown || !strings.Contains(out, "kept test-2: could not check: no default branch") {
		t.Fatalf("unknown: exit %d, %q", code, out)
	}
}

// Prune deletes only the local branch, says so, and makes no claim about a remote copy,
// which it leaves untouched (bees-1.34).
func TestPruneSaysOnlyWhatItDidLocally(t *testing.T) {
	useTempLogbook(t)
	saved := newFleet
	t.Cleanup(func() { newFleet = saved })
	newFleet = func(f worker.Fleet) worker.Fleet { return f } // no gh

	repo := repoWithBranch(t, "main", "pushed")
	origin := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "--bare", origin)
	git("remote", "add", "origin", origin)
	git("push", "-q", "origin", "pushed")
	git("merge", "-q", "--ff-only", "pushed")
	git("branch", "local-only", "main")
	endedWorker(t, "pushed", repo)
	endedWorker(t, "local-only", repo)

	out, _, code := bs(t, "prune", "--dry-run")
	if code != 0 ||
		!strings.Contains(out, "would delete local branch pushed: merged, its commits are on main\n") ||
		!strings.Contains(out, "would delete local branch local-only: merged, its commits are on main\n") {
		t.Fatalf("dry run: exit %d, %q", code, out)
	}
	out, _, code = bs(t, "prune")
	if code != 0 ||
		!strings.Contains(out, "deleted local branch pushed: merged, its commits are on main\n") ||
		!strings.Contains(out, "deleted local branch local-only: merged, its commits are on main\n") {
		t.Fatalf("prune: exit %d, %q", code, out)
	}
	if strings.Contains(out, "origin") {
		t.Fatalf("prune claims something about a remote: %q", out)
	}
	if err := exec.Command("git", "-C", origin, "rev-parse", "--verify", "--quiet", "refs/heads/pushed").Run(); err != nil {
		t.Fatalf("origin lost pushed: %v", err)
	}
}

// A branch in use whose merge couldn't be checked reports both and exits 4, the
// probe-failure code; a gone worktree comes with the command that frees the branch.
func TestPruneReportsABranchInUseWithItsProbeFailure(t *testing.T) {
	useTempLogbook(t)
	saved := newFleet
	t.Cleanup(func() { newFleet = saved })
	newFleet = func(f worker.Fleet) worker.Fleet { return f } // no gh

	repo := repoWithBranch(t, "trunk", "test-3") // no main: can't check
	wt := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", wt, "test-3").CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	endedWorker(t, "test-3", repo)
	out, _, code := bs(t, "prune")
	if code != exitKeptUnknown || !strings.Contains(out, "kept test-3: in use in worktree") || !strings.Contains(out, "could not check: no default branch") {
		t.Fatalf("in use, unknown: exit %d, %q", code, out)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if out, _, _ := bs(t, "prune"); !strings.Contains(out, "which is gone; git worktree prune frees it") {
		t.Fatalf("gone worktree: %q", out)
	}
}
