// Package scenario is bsx's scenario suite (ADR-0010 Tests): every check of the pilot's
// round-11 sweep, every pilot bug, and ADR-0010's goal and Rule G rules, each a test with
// assertions on entries, output and exit codes. A scenario whose feature lands later is
// skipped, naming the bead that un-skips it.
package scenario_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/cli"
	"github.com/jmmshn/bees/cmd/bsx/internal/core"
	"github.com/jmmshn/bees/cmd/bsx/internal/fake"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// later skips a scenario until bead lands its feature.
func later(t *testing.T, bead, feature string) {
	t.Helper()
	t.Skip(bead + ": " + feature)
}

// toy is one throwaway project on a fake herdr --session server: a real git repo, its
// session config dir, a logbook dir, and beesfake as the harness.
type toy struct {
	t       *testing.T
	session string
	repo    string
	env     []string // the keeper's shell: outside any pane
	herdr   *fake.Herdr
	gh      *fake.GH
	harness fake.Harness
	beads   *fake.Beads
	ports   cli.Ports
	logbook string
	config  string // the session's config dir
}

const project = "toy"

func newToy(t *testing.T) *toy {
	t.Helper()
	root := t.TempDir()
	state, err := os.MkdirTemp("", "bf") // short: socket paths must fit sun_path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	home, cfg := filepath.Join(root, "home"), filepath.Join(root, "config")
	repo := filepath.Join(root, "toy")
	x := &toy{t: t, session: "toy", repo: repo, herdr: nil, harness: fake.Harness{State: state}, beads: &fake.Beads{},
		logbook: filepath.Join(root, "logbooks", project+".db")}
	x.config = filepath.Join(cfg, "bees", "sessions", x.session)
	for _, d := range []string{home, repo, x.config} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	x.write(filepath.Join(x.config, "config.toml"), `[paths]
logbook_dir = "`+filepath.Join(root, "logbooks")+`"
worktrees = "`+filepath.Join(root, "toy-WT")+`"
`)
	x.git(repo, "init", "-q", "-b", "main")
	x.write(filepath.Join(repo, "README"), "toy\n")
	x.git(repo, "add", ".")
	x.git(repo, "commit", "-qm", "init")
	relay := filepath.Join(state, "relay.sock")
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + cfg,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}
	x.env = append(base, "HERDR_SESSION="+x.session)
	// beesfake, built as claude, is first on the panes' PATH: bs queen execs it.
	pane := append(slices.Clone(base), "PATH="+filepath.Dir(fake.Beesfake(t))+":"+os.Getenv("PATH"), "BEESFAKE_RELAY="+relay)
	x.herdr = &fake.Herdr{Home: home, Programs: map[string]string{"claude": fake.Beesfake(t)}, Env: pane}
	t.Cleanup(x.herdr.Close)
	x.gh = &fake.GH{PRs: map[string]*fake.PR{}}
	x.ports = cli.Ports{Run: fake.Commands{Herdr: x.herdr, GH: x.gh, Real: port.Exec{}}, Harness: x.harness, Proc: x.herdr, Beads: x.beads,
		CacheDir: filepath.Join(root, "cache"),
		Timing:   core.Timing{Confirm: 10 * time.Second, Poll: 20 * time.Millisecond, SecondLook: 100 * time.Millisecond, Stop: time.Second}}
	// A bs call a pane's beesfake makes runs here, as that process: its env, its pid.
	// bs queen's exec is handed back for the caller to make: it keeps the pid recorded.
	fake.Relay(t, relay, func(c fake.Call) fake.Reply {
		var out, errb bytes.Buffer
		var r fake.Reply
		ports := x.ports
		ports.Exec = func(dir string, env, argv []string) error {
			r.Exec, r.ExecDir, r.ExecEnv = argv, dir, env
			return nil
		}
		r.Code = cli.Run(context.Background(), cli.Env{Vars: c.Env, Dir: c.Cwd, Self: c.PID, Stdin: strings.NewReader("")}, ports, c.Argv, &out, &errb)
		r.Stdout, r.Stderr = out.String(), errb.String()
		return r
	})
	return x
}

func (x *toy) write(path, s string) {
	x.t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		x.t.Fatal(err)
	}
}

func (x *toy) git(dir string, args ...string) string {
	x.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		x.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// result is one bsx call's outcome.
type result struct {
	code        int
	out, stderr string
}

func (r result) String() string { return r.out + r.stderr }

// keeper runs bsx args from the keeper's shell (outside any pane), as queen-toy where a
// command needs a session.
func (x *toy) keeper(args ...string) result {
	x.t.Helper()
	var out, errb bytes.Buffer
	code := cli.Run(context.Background(), cli.Env{Vars: x.env, Dir: x.repo, Self: os.Getpid(), Stdin: strings.NewReader("")}, x.ports, args, &out, &errb)
	return result{code, out.String(), errb.String()}
}

// queen is keeper with --as queen-toy put after the command.
func (x *toy) queen(args ...string) result {
	x.t.Helper()
	return x.keeper(append([]string{args[0], "--as", "queen-toy"}, args[1:]...)...)
}

// worker runs bsx args from inside worker name's pane, as a child of its harness
// process, and returns the nth result of its script.
func (x *toy) worker(name string, n int, args ...string) result {
	x.t.Helper()
	x.harness.Step(x.t, name, append([]string{"bs"}, args...)...)
	r := x.harness.Await(x.t, name, n)
	return result{r.Code, r.Stdout, r.Stderr}
}

// bead adds a bead to the fake tracker.
func (x *toy) bead(id, title string) {
	x.beads.Add(port.Bead{ID: id, Title: title, Description: "do " + title})
}

// open opens the toy's logbook read-only, for assertions on entries.
func (x *toy) open() *logbook.Logbook {
	x.t.Helper()
	lb, err := logbook.OpenReadOnly(context.Background(), x.logbook)
	if err != nil {
		x.t.Fatal(err)
	}
	x.t.Cleanup(func() { lb.Close() })
	return lb
}

// spawn spawns bead as queen-toy with goals and fails the test unless it worked.
func (x *toy) spawn(bead string, goals ...string) logbook.Spawn {
	x.t.Helper()
	x.bead(bead, "work on "+bead)
	args := []string{"spawn"}
	for _, g := range goals {
		args = append(args, "--goal", g)
	}
	if r := x.queen(append(args, bead)...); r.code != 0 {
		x.t.Fatalf("spawn %s: exit %d: %s", bead, r.code, r)
	}
	sp, ok, err := x.open().LatestSpawn(context.Background(), core.Name(bead))
	if err != nil || !ok {
		x.t.Fatalf("spawn %s not recorded: %v", bead, err)
	}
	return sp
}

// commit makes a commit in dir.
func (x *toy) commit(dir, file string) {
	x.t.Helper()
	x.write(filepath.Join(dir, file), file+"\n")
	x.git(dir, "add", file)
	x.git(dir, "commit", "-qm", "add "+file)
}

func must(t *testing.T, r result, code int, want ...string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d, want %d:\n%s", r.code, code, r)
	}
	for _, w := range want {
		if !strings.Contains(r.String(), w) {
			t.Fatalf("output lacks %q:\n%s", w, r)
		}
	}
}
