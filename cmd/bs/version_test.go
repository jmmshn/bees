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
	git := func(args ...string) string { return gitIn(t, dir, args...) }
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

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// withBuild runs bs as if built from rev and installed outside any checkout, called
// by session (empty: the keeper, outside Claude) in project bees.
func withBuild(t *testing.T, rev, session string) {
	t.Helper()
	oldRev, oldExe := buildRevision, executable
	buildRevision = func() string { return rev }
	executable = func() (string, error) { return "/usr/local/bin/bs", nil }
	t.Cleanup(func() { buildRevision, executable = oldRev, oldExe })
	t.Setenv("HOME", t.TempDir()) // no real Claude registry: the session walk finds nothing
	t.Setenv("BS_SESSION", session)
	t.Setenv("BS_PROJECT", "bees")
}

func TestStaleBsInItsOwnRepoWarns(t *testing.T) {
	dir, revs := fakeSourceRepo(t)
	t.Chdir(dir)
	withBuild(t, revs[0], "")
	out, stderr, code := bs(t, "version")
	if code != 0 || !strings.Contains(out, revs[0][:12]) {
		t.Fatalf("version: exit %d, stdout %q", code, out)
	}
	if !strings.Contains(stderr, "1 commit behind main") || !strings.Contains(stderr, "go install ./cmd/bs") {
		t.Fatalf("stderr = %q, want a stale-binary warning", stderr)
	}
}

func TestBranchBuildWarns(t *testing.T) {
	dir, _ := fakeSourceRepo(t)
	gitIn(t, dir, "checkout", "-q", "-b", "side")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "side")
	side := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "checkout", "-q", "main")
	t.Chdir(dir)
	withBuild(t, side, "queen-bees")
	if _, stderr, _ := bs(t, "version"); !strings.Contains(stderr, "not on main") || !strings.Contains(stderr, "go install ./cmd/bs") {
		t.Fatalf("stderr = %q, want a branch-build warning telling the queen to install from main", stderr)
	}
}

func TestStaleBsTellsAWorkerToAskTheQueen(t *testing.T) {
	dir, revs := fakeSourceRepo(t)
	t.Chdir(dir)
	withBuild(t, revs[0], "bees-1-26")
	_, stderr, _ := bs(t, "version")
	if !strings.Contains(stderr, "bs send queen-bees") || strings.Contains(stderr, "go install") {
		t.Fatalf("stderr = %q, want the worker told to ask the queen, not to go install", stderr)
	}
}

func TestWorktreeBuildChecksAgainstHead(t *testing.T) {
	dir, revs := fakeSourceRepo(t)
	t.Chdir(dir)
	for _, c := range []struct {
		rev, want string
	}{
		{revs[1], ""}, // HEAD: quiet, even for a worker
		{revs[0], "go build -o bin/bs ./cmd/bs"},
	} {
		withBuild(t, c.rev, "bees-1-26")
		executable = func() (string, error) { return filepath.Join(dir, "bin", "bs"), nil }
		_, stderr, _ := bs(t, "version")
		if (c.want == "" && stderr != "") || !strings.Contains(stderr, c.want) {
			t.Errorf("built from %.7s: stderr = %q, want %q", c.rev, stderr, c.want)
		}
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
			withBuild(t, c.rev, "")
			if _, stderr, code := bs(t, "version"); code != 0 || stderr != "" {
				t.Fatalf("exit %d, stderr %q; want no warning", code, stderr)
			}
		})
	}
}
