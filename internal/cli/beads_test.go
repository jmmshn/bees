package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/port"
)

// bdStub answers bd from canned output and records each call; everything else (git) runs
// for real.
type bdStub struct {
	out   map[string]string // by subcommand
	calls [][]string
}

func (b *bdStub) Run(ctx context.Context, dir string, argv ...string) ([]byte, error) {
	if argv[0] != "bd" {
		return port.Exec{}.Run(ctx, dir, argv...)
	}
	b.calls = append(b.calls, argv[1:])
	return []byte(b.out[argv[1]]), nil
}

func (r *rig) runWith(ports Ports, extra []string, args ...string) (int, string, string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	ports.CacheDir = filepath.Join(r.home, "cache")
	code := Run(context.Background(), Env{Vars: append(append([]string{}, r.env...), extra...), Dir: r.repo, Self: os.Getpid()}, ports, args, &out, &errb)
	return code, out.String(), errb.String()
}

// bs epic takes the number after the project's highest, children and closed beads
// included, and never another project's.
func TestEpicTakesTheNextNumber(t *testing.T) {
	r := newRig(t)
	bd := &bdStub{out: map[string]string{"list": `[{"id":"mytoy-2"},{"id":"mytoy-4.1"},{"id":"mytoy2-9"},{"id":"other-7"}]`}}
	code, out, stderr := r.runWith(Ports{Run: bd}, nil, "epic", "Big", "plan")
	if code != 0 || out != "mytoy-5\n" || !strings.Contains(stderr, "bs bead new --parent mytoy-5") {
		t.Fatalf("exit %d %q %q", code, out, stderr)
	}
	want := "create --silent --force --type epic --id mytoy-5 --title Big plan"
	if got := strings.Join(bd.calls[len(bd.calls)-1], " "); got != want {
		t.Fatalf("bd %s, want bd %s", got, want)
	}
}

// bs bead ls stays inside the project; bs bead new files only under one of its beads.
func TestBeadStaysInTheProject(t *testing.T) {
	r := newRig(t)
	bd := &bdStub{out: map[string]string{"query": "mytoy-1 one\n", "create": "mytoy-1.3\n"}}
	if code, out, stderr := r.runWith(Ports{Run: bd}, nil, "bead", "ls", "priority<=1"); code != 0 || out != "mytoy-1 one\n" {
		t.Fatalf("ls: exit %d %q %q", code, out, stderr)
	}
	if got := bd.calls[0]; got[3] != `id="mytoy-*" AND status!=closed AND (priority<=1)` {
		t.Fatalf("ls query %q", got[3])
	}
	// Flags may follow the title, as the old bs took them (a live bakeoff queen typed them so).
	if code, out, stderr := r.runWith(Ports{Run: bd}, nil, "bead", "new", "--parent", "mytoy-1", "Fix", "it", "--priority", "1"); code != 0 || out != "mytoy-1.3\n" {
		t.Fatalf("new: exit %d %q %q", code, out, stderr)
	}
	if got := strings.Join(bd.calls[1], " "); got != "create --silent --force --parent mytoy-1 --title Fix it --priority 1" {
		t.Fatalf("bd %s", got)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"bead", "new", "Fix"}, "needs --parent mytoy-N"},
		{[]string{"bead", "new", "--parent", "other-1", "Fix"}, "other-1 is not a mytoy bead"},
		{[]string{"bead", "new", "--parent", "mytoy2-1", "Fix"}, "mytoy2-1 is not a mytoy bead"},
		{[]string{"bead"}, "usage"},
		{[]string{"bead", "ls", "priority<=1) OR (status=open"}, "parentheses don't balance"},
		{[]string{"bead", "ls", "(priority<=1"}, "parentheses don't balance"},
		{[]string{"epic"}, "usage"},
	} {
		if code, _, stderr := r.runWith(Ports{Run: bd}, nil, c.args...); code != core.ExitRefused || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d %q", c.args, code, stderr)
		}
	}
	if len(bd.calls) != 2 {
		t.Fatalf("a refused call reached bd: %v", bd.calls[2:])
	}
}

// fakeSourceRepo makes a checkout declaring bs's module, main two commits long.
func fakeSourceRepo(t *testing.T) (string, []string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+ownModule+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q", "-b", "main")
	var revs []string
	for _, m := range []string{"one", "two"} {
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", m)
		revs = append(revs, gitIn(t, dir, "rev-parse", "HEAD"))
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

// version runs bs version in dir, built from rev and installed at exe.
func version(t *testing.T, dir, rev, exe string) (int, string, string) {
	t.Helper()
	r := newRig(t)
	r.repo = dir
	return r.runWith(Ports{Run: port.Exec{}, Revision: rev, Exe: exe}, nil, "version")
}

// A shared bs behind main, or built from a branch, says so and says who installs it
// (motif 14): the shared bs is the queen's to install, from main.
func TestAStaleSharedBsWarns(t *testing.T) {
	dir, revs := fakeSourceRepo(t)
	code, out, stderr := version(t, dir, revs[0], "/usr/local/bin/bs")
	if code != 0 || !strings.Contains(out, revs[0]) || !strings.Contains(stderr, "1 commit behind main") || !strings.Contains(stderr, "go install ./cmd/bs") {
		t.Fatalf("behind: exit %d %q %q", code, out, stderr)
	}
	gitIn(t, dir, "checkout", "-q", "-b", "side")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "side")
	side := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "checkout", "-q", "main")
	if _, _, stderr := version(t, dir, side, "/usr/local/bin/bs"); !strings.Contains(stderr, "not on main") {
		t.Fatalf("branch build: %q", stderr)
	}
}

// A worktree build is checked against its checkout's HEAD.
func TestAWorktreeBuildIsCheckedAgainstHead(t *testing.T) {
	dir, revs := fakeSourceRepo(t)
	exe := filepath.Join(dir, "bin", "bs")
	if _, _, stderr := version(t, dir, revs[1], exe); strings.Contains(stderr, "built from") {
		t.Fatalf("HEAD build warned: %q", stderr)
	}
	if _, _, stderr := version(t, dir, revs[0], exe); !strings.Contains(stderr, "go build -o bin/bs ./cmd/bs") {
		t.Fatalf("old build: %q", stderr)
	}
}

// bs stays quiet when it can't tell, or is current.
func TestACurrentOrForeignBsDoesNotWarn(t *testing.T) {
	dir, revs := fakeSourceRepo(t)
	for _, c := range []struct{ name, dir, rev string }{
		{"built from main", dir, revs[1]},
		{"not built from this repo", dir, "0123456789abcdef0123456789abcdef01234567"},
		{"no vcs stamp", dir, ""},
		{"outside the source repo", t.TempDir(), revs[0]},
	} {
		if c.name == "built from main" { // a repo with no main can't tell, so says nothing
			nomain, nrevs := fakeSourceRepo(t)
			gitIn(t, nomain, "branch", "-m", "main", "trunk")
			if code, _, stderr := version(t, nomain, nrevs[0], "/usr/local/bin/bs"); code != 0 || strings.Contains(stderr, "built from") {
				t.Errorf("no main: exit %d %q", code, stderr)
			}
		}
		if code, _, stderr := version(t, c.dir, c.rev, "/usr/local/bin/bs"); code != 0 || strings.Contains(stderr, "built from") {
			t.Errorf("%s: exit %d %q", c.name, code, stderr)
		}
	}
}

// A worker is told to ask its queen, never to install bs itself.
func TestAStaleBsTellsAWorkerToAskTheQueen(t *testing.T) {
	worker := &settings{Project: "bees", Caller: core.Caller{Name: "bees-1-2", Role: core.WorkerR}}
	queen := &settings{Project: "bees", Caller: core.Caller{Name: "queen-bees", Role: core.QueenR}}
	if got := staleFix(worker); !strings.Contains(got, `bs send queen-bees "bs is stale"`) || strings.Contains(got, "go install") {
		t.Errorf("worker: %q", got)
	}
	for _, s := range []*settings{queen, nil, {}} {
		if got := staleFix(s); !strings.Contains(got, "go install ./cmd/bs") {
			t.Errorf("%+v: %q", s, got)
		}
	}
}
