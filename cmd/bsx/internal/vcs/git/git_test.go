package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/port"
	"github.com/jmmshn/bees/cmd/bsx/internal/vcs/git"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_EDITOR=true",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A worktree's commits are what wrote one there: commit, amend, a merge commit, a
// cherry-pick; never what a reset, checkout or fast-forward moved HEAD to.
func TestCommitsAreOnlyThoseMadeThere(t *testing.T) {
	root := t.TempDir()
	repo, wt := filepath.Join(root, "r"), filepath.Join(root, "w")
	run(t, root, "init", "-q", "-b", "main", repo)
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	run(t, repo, "branch", "other")
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "elsewhere")
	elsewhere := run(t, repo, "rev-parse", "HEAD")
	since := time.Now().Add(-time.Second)
	run(t, repo, "worktree", "add", "-q", "-b", "w", wt, "other")
	run(t, wt, "commit", "-q", "--allow-empty", "-m", "a")
	run(t, wt, "commit", "-q", "--amend", "--allow-empty", "-m", "b")
	amended := run(t, wt, "rev-parse", "HEAD")
	run(t, wt, "merge", "-q", "--no-ff", "--no-edit", "main")
	merge := run(t, wt, "rev-parse", "HEAD")
	run(t, wt, "checkout", "-q", "-b", "ff", "other")
	run(t, wt, "merge", "-q", "--ff-only", "main") // a fast-forward to elsewhere
	run(t, wt, "reset", "-q", "--hard", "main")
	v := git.VCS{Run: port.Exec{}}
	got, err := v.Commits(context.Background(), wt, since)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, amended) || !slices.Contains(got, merge) || slices.Contains(got, elsewhere) || len(got) != 3 {
		t.Fatalf("commits = %q; want a, b (amended %s) and the merge %s, never %s", got, amended, merge, elsewhere)
	}
	if late, _ := v.Commits(context.Background(), wt, time.Now().Add(time.Hour)); len(late) != 0 {
		t.Fatalf("commits since later = %q", late)
	}
}

// A pull's rebase replays the workspace's own commits, whatever the pull's spelling (probed
// on git 2.4x: the reflog says the command line, "pull -q --rebase origin main (pick)", or
// "pull -q (pick)" with pull.rebase set); the commits it pulled are never the workspace's.
func TestPullRebasePicksAreCommitsMadeThere(t *testing.T) {
	root := t.TempDir()
	up, wt := filepath.Join(root, "up"), filepath.Join(root, "w")
	run(t, root, "init", "-q", "-b", "main", up)
	run(t, up, "commit", "-q", "--allow-empty", "-m", "init")
	run(t, root, "clone", "-q", up, wt)
	since := time.Now().Add(-time.Second)
	var pulled []string
	for i, pull := range [][]string{
		{"pull", "-q", "--rebase", "origin", "main"},
		{"-c", "pull.rebase=true", "pull", "-q"},
		{"pull", "-q", "--rebase=merges"},
		{"pull", "--rebase"},
	} {
		run(t, up, "commit", "-q", "--allow-empty", "-m", "up")
		pulled = append(pulled, run(t, up, "rev-parse", "HEAD"))
		f := filepath.Join(wt, "f"+string(rune('a'+i)))
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		run(t, wt, "add", f)
		run(t, wt, "commit", "-q", "-m", "mine")
		run(t, wt, pull...)
	}
	got, err := git.VCS{Run: port.Exec{}}.Commits(context.Background(), wt, since)
	if err != nil {
		t.Fatal(err)
	}
	head := run(t, wt, "rev-parse", "HEAD")
	if !slices.Contains(got, head) {
		t.Errorf("commits = %q: the last pull's pick %s is missing", got, head)
	}
	for _, p := range pulled {
		if slices.Contains(got, p) {
			t.Errorf("commits = %q hold %s, which was pulled", got, p)
		}
	}
	// 4 commits, then 1+2+3+4 picks.
	if len(got) != 14 {
		t.Errorf("commits = %d, want 14: %q", len(got), got)
	}
}
