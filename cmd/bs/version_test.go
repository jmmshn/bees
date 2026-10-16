package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSourceRepo makes a git repo declaring bs's module, with two commits on main, and
// returns their revisions, oldest first.
func fakeSourceRepo(t *testing.T) (dir string, revs []string) {
	t.Helper()
	dir = t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+ownModule+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "go.mod")
	for _, m := range []string{"one", "two"} {
		git("commit", "-q", "--allow-empty", "-m", m)
		revs = append(revs, git("rev-parse", "HEAD"))
	}
	return dir, revs
}

func withBuild(t *testing.T, rev string) {
	old := buildRevision
	buildRevision = func() string { return rev }
	t.Cleanup(func() { buildRevision = old })
}

func TestStaleBsInItsOwnRepoWarns(t *testing.T) {
	dir, revs := fakeSourceRepo(t)
	t.Chdir(filepath.Join(dir))
	withBuild(t, revs[0])
	out, stderr, code := bs(t, "version")
	if code != 0 || !strings.Contains(out, revs[0][:12]) {
		t.Fatalf("version: exit %d, stdout %q", code, out)
	}
	if !strings.Contains(stderr, "1 commit behind main") || !strings.Contains(stderr, "go install ./cmd/bs") {
		t.Fatalf("stderr = %q, want a stale-binary warning", stderr)
	}
}

func TestCurrentOrForeignBsDoesNotWarn(t *testing.T) {
	dir, revs := fakeSourceRepo(t)
	for _, c := range []struct{ name, cwd, rev string }{
		{"built from main", dir, revs[1]},
		{"not built from this repo", dir, "0123456789abcdef0123456789abcdef01234567"},
		{"no vcs stamp", dir, ""},
		{"outside the source repo", t.TempDir(), revs[0]},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Chdir(c.cwd)
			withBuild(t, c.rev)
			if _, stderr, code := bs(t, "version"); code != 0 || stderr != "" {
				t.Fatalf("exit %d, stderr %q; want no warning", code, stderr)
			}
		})
	}
}
