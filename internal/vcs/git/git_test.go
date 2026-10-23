package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

// A push= worker pushes its own branch onto the target's head ref (git push origin
// HEAD:feature): git moves refs/remotes/origin/feature, so Pushed reads the push under the
// target's name while the host lags (ADR-0010 Amendment 4).
func TestPushedReadsAPushOntoAnotherBranchsName(t *testing.T) {
	root := t.TempDir()
	up, wt := filepath.Join(root, "up.git"), filepath.Join(root, "w")
	run(t, root, "init", "-q", "--bare", "-b", "main", up)
	run(t, root, "clone", "-q", up, wt)
	run(t, wt, "commit", "-q", "--allow-empty", "-m", "init")
	run(t, wt, "push", "-q", "origin", "HEAD:feature")
	run(t, wt, "switch", "-q", "-c", "toy-1")
	run(t, wt, "commit", "-q", "--allow-empty", "-m", "fix")
	tip := run(t, wt, "rev-parse", "HEAD")
	run(t, wt, "push", "-q", "origin", "HEAD:feature")
	v := git.VCS{Run: port.Exec{}}
	got, ok, err := v.Pushed(context.Background(), wt, "feature")
	if err != nil || !ok || got != tip {
		t.Fatalf("Pushed(feature) = %q, %v, %v; want %s", got, ok, err, tip)
	}
}
