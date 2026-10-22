package git_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/vcs/git"
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

// A pull's rebase writes nothing of the worker's: its picks are copies, under any of the
// pull's spellings, and what it pulled is someone else's. The copies match what the
// worker wrote by patch-id; the pulled commits don't (keeper, 2026-10-22).
func TestARebasedCopyMatchesByPatchIDAndAPulledCommitDoesnt(t *testing.T) {
	root := t.TempDir()
	up, wt := filepath.Join(root, "up"), filepath.Join(root, "w")
	run(t, root, "init", "-q", "-b", "main", up)
	run(t, up, "commit", "-q", "--allow-empty", "-m", "init")
	run(t, root, "clone", "-q", up, wt)
	since := time.Now().Add(-time.Second)
	var written, pulled []string
	for i, pull := range [][]string{
		{"pull", "-q", "--rebase", "origin", "main"},
		{"-c", "pull.rebase=true", "pull", "-q"},
	} {
		for _, d := range []string{up, wt} {
			f := filepath.Join(d, fmt.Sprintf("%s%d", filepath.Base(d), i))
			if err := os.WriteFile(f, []byte(f), 0o644); err != nil {
				t.Fatal(err)
			}
			run(t, d, "add", f)
			run(t, d, "commit", "-q", "-m", "c")
		}
		pulled = append(pulled, run(t, up, "rev-parse", "HEAD"))
		written = append(written, run(t, wt, "rev-parse", "HEAD"))
		run(t, wt, pull...)
	}
	v := git.VCS{Run: port.Exec{}}
	ctx := context.Background()
	got, err := v.Commits(ctx, wt, since)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{written[1], written[0]}) {
		t.Fatalf("commits = %q, want only the two written, %q", got, written)
	}
	copies := strings.Fields(run(t, wt, "rev-list", "HEAD", "--not", pulled[1]))
	all := append(append(slices.Clone(written), pulled...), copies...)
	ids, missing, err := v.PatchIDs(ctx, wt, append(all, strings.Repeat("0", 40)))
	if err != nil || len(missing) != 1 {
		t.Fatalf("PatchIDs: %v, missing %q", err, missing)
	}
	// The last pull copied both written commits (newest first).
	for i, c := range copies {
		w := written[len(written)-1-i]
		if len(copies) != 2 || c == w || ids[c] != ids[w] || ids[c] == "" {
			t.Errorf("copies %q: %s's patch-id %q, want %s's %q", copies, c, ids[c], w, ids[w])
		}
	}
	for _, p := range pulled {
		for _, w := range written {
			if ids[p] == ids[w] {
				t.Errorf("pulled %s shares %s's patch-id", p, w)
			}
		}
	}
}
