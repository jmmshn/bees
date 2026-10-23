package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/harness/agy"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
)

// rig is a keeper's shell on herdr session toy: a home, a config root and a git repo.
type rig struct {
	t                *testing.T
	home, cfg, repo  string
	env              []string
	session, logbook string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	// git reports the repo with symlinks resolved (macOS: /var is /private/var).
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, home: filepath.Join(root, "home"), cfg: filepath.Join(root, "cfg"), repo: filepath.Join(root, "my-toy"),
		logbook: filepath.Join(root, "logbooks")}
	r.session = filepath.Join(r.cfg, "bees", "sessions", "toy")
	r.env = []string{"HOME=" + r.home, "XDG_CONFIG_HOME=" + r.cfg, "HERDR_SESSION=toy", "PATH=" + os.Getenv("PATH")}
	for _, d := range []string{r.home, r.repo} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "init", "-q", r.repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	r.write("config.toml", "[paths]\nlogbook_dir = \""+r.logbook+"\"\n")
	return r
}

// start makes the rig project's logbook, as a first bs queen would (R48).
func (r *rig) start() {
	r.t.Helper()
	lb, _, err := logbook.Create(context.Background(), filepath.Join(r.logbook, "mytoy.db"))
	if err != nil {
		r.t.Fatal(err)
	}
	lb.Close()
}

func (r *rig) write(name, body string) {
	r.t.Helper()
	path := filepath.Join(r.session, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// run runs bs args in dir with env plus extra, and returns its exit code and output.
func (r *rig) run(dir string, extra []string, args ...string) (int, string, string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	ports := Ports{Run: port.Exec{}, CacheDir: filepath.Join(r.home, "cache")}
	code := Run(context.Background(), Env{Vars: append(append([]string{}, r.env...), extra...), Dir: dir, Self: os.Getpid()}, ports, args, &out, &errb)
	return code, out.String(), errb.String()
}

func wantIn(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Errorf("%q lacks %q", s, p)
		}
	}
}

// A herdr --session toy never opens the real logbooks: not through ~/.local/share/bees,
// $XDG_DATA_HOME/bees, or a symlink to either. The default session runs on them.
func TestRefusesTheRealLogbookDir(t *testing.T) {
	r := newRig(t)
	data := t.TempDir()
	real := filepath.Join(r.home, ".local", "share", "bees")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{real, link, filepath.Join(data, "bees")} {
		r.write("config.toml", "[paths]\nlogbook_dir = \""+d+"\"\n")
		code, _, stderr := r.run(r.repo, []string{"XDG_DATA_HOME=" + data}, "status")
		if code != 2 || !strings.Contains(stderr, "toy's paths.logbook_dir is the real logbooks' dir") {
			t.Errorf("logbook_dir %s: exit %d %s", d, code, stderr)
		}
	}
	r.write("config.toml", "[paths]\nlogbook_dir = \""+r.logbook+"\"\n")
	r.start()
	if code, _, stderr := r.run(r.repo, nil, "status"); code != 0 {
		t.Fatalf("a toy's own logbook dir: exit %d %s", code, stderr)
	}
	lb, _, err := logbook.Create(context.Background(), filepath.Join(real, "mytoy.db")) // the default session's own
	if err != nil {
		t.Fatal(err)
	}
	lb.Close()
	for _, s := range []string{"HERDR_SESSION=", "HERDR_SESSION=default"} {
		code, _, stderr := r.run(r.repo, []string{s}, "status")
		if code != 0 || strings.Contains(stderr, "warning") {
			t.Errorf("%s: exit %d %s", s, code, stderr)
		}
	}
}

// Rule G: with the session's config dir missing, every command that opens a logbook
// refuses (exit 2) and touches none, since the defaults would name the real one
// (bees-12.26); config and doctor run, warning each call.
func TestMissingSessionDir(t *testing.T) {
	r := newRig(t)
	if err := os.RemoveAll(r.session); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"spawn", "--goal", "branch", "toy-1"}, {"send", "--as", "x", "y", "hi"}, {"inbox", "--as", "x"},
		{"end", "--as", "queen-toy", "toy-1"}, {"prune", "--as", "queen-toy"}, {"done", "--as", "toy-1"}, {"status"}} {
		code, out, stderr := r.run(r.repo, nil, args...)
		if code != 2 || out != "" {
			t.Errorf("%v: exit %d, out %q", args, code, out)
		}
		wantIn(t, stderr, "warning: no config dir for this herdr session toy", "refused", "won't guess a logbook; nothing was read or written")
	}
	if _, err := os.Stat(r.logbook); !os.IsNotExist(err) {
		t.Errorf("a refused command made the logbook dir: %v", err)
	}
	code, out, stderr := r.run(r.repo, nil, "config")
	if code != 0 {
		t.Errorf("config: exit %d %s", code, stderr)
	}
	wantIn(t, stderr, "warning: no config dir")
	wantIn(t, out, r.session+", herdr session toy (missing", `paths.logbook_dir = "~/.local/share/bees"  # default; global file only`)
	code, out, _ = r.run(r.repo, nil, "doctor")
	if code != 1 {
		t.Errorf("doctor: exit %d", code)
	}
	wantIn(t, out, "FAIL config dir: "+r.session+" is missing")
}

func TestConfigShowsEverySourceAndTheSessionDir(t *testing.T) {
	r := newRig(t)
	r.write("projects/mytoy.toml", "[worker.claude_code]\nmodel = \"sonnet\"\n[goal.cl]\nhandoff = [\"clean\"]\n")
	code, out, stderr := r.run(filepath.Join(r.repo), nil, "config")
	if code != 0 {
		t.Fatalf("exit %d %s", code, stderr)
	}
	wantIn(t, out,
		"# config dir: "+r.session+", herdr session toy\n",
		"# project file: "+filepath.Join(r.session, "projects", "mytoy.toml")+"\n",
		"# project: mytoy (the git repo at "+r.repo+")",
		"# logbook: "+filepath.Join(r.logbook, "mytoy.db"),
		"# worktrees: "+r.repo+"-WT",
		"# workspace kind: git_worktree (worked out: the repo is a git checkout's top level)",
		`harness = "claude_code"  # default; global file only`,
		`paths.logbook_dir = "`+r.logbook+`"  # global `+filepath.Join(r.session, "config.toml")+"; global file only",
		`worker.claude_code.model = "sonnet"  # project `,
		`goal.cl.handoff = ["clean"]  # project `)
}

// bees-12.47: an exported BS_PROJECT never silently beats the repo bs runs in, in a herdr
// pane no entry recorded too (bees-13.14). Inside a recorded pane every BS_* is ignored,
// with a warning: TestBSVariablesAreIgnoredInsideARecordedPane.
func TestBSProject(t *testing.T) {
	r := newRig(t)
	code, _, stderr := r.run(r.repo, []string{"BS_PROJECT=bees"}, "config")
	if code != 2 {
		t.Errorf("mismatch: exit %d", code)
	}
	wantIn(t, stderr, "BS_PROJECT=bees names another project than the repo you are in (mytoy)", "next: env -u BS_PROJECT")
	code, _, stderr = r.run(r.repo, []string{"BS_PROJECT=bees", "HERDR_PANE_ID=w1:p1"}, "config")
	if code != 2 {
		t.Errorf("in an unrecorded pane: exit %d %s", code, stderr)
	}
	wantIn(t, stderr, "BS_PROJECT=bees names another project than the repo you are in (mytoy)")
	// bees-12.45: doctor names the variable and the way out, never "no repo".
	code, out, _ := r.run(r.repo, []string{"BS_PROJECT=bees"}, "doctor")
	if code != 1 {
		t.Errorf("doctor: exit %d", code)
	}
	wantIn(t, out, "FAIL project: BS_PROJECT=bees names another project than the repo you are in (mytoy)", "the checks below run on the global file alone", "next: env -u BS_PROJECT", "WARN env: BS_PROJECT is set and overrides the config here (from the keeper's shell), for every bs command run from this shell")
	_, out, _ = r.run(r.repo, []string{"BS_LOGBOOK=/x"}, "doctor")
	wantIn(t, out, "WARN env: BS_LOGBOOK is set; bs doesn't read it (an older bs did)")
	// An unrecorded pane is outside one (bees-13.14): doctor names the old bs's pins as unread.
	_, out, _ = r.run(r.repo, []string{"BS_NAME=bees-3", "HERDR_PANE_ID=w1:p1"}, "doctor")
	wantIn(t, out, "WARN env: BS_NAME is set; bs doesn't read it")
	_, out, _ = r.run(r.repo, []string{"BS_NAME=bees-3", "BS_LOGBOOK_DIR=" + r.logbook}, "doctor")
	wantIn(t, out, "WARN env: BS_NAME is set; bs doesn't read it", "WARN env: BS_LOGBOOK_DIR is set and overrides the config here")
	code, out, _ = r.run("/", []string{"BS_PROJECT=other"}, "config")
	if code != 0 {
		t.Errorf("outside any repo: exit %d", code)
	}
	wantIn(t, out, "# project: other (env BS_PROJECT)")
	_, out, _ = r.run("/", []string{"BS_PROJECT=other", "HERDR_PANE_ID=w1:p1"}, "config")
	wantIn(t, out, "# project: other (env BS_PROJECT)") // an unrecorded pane is outside one (bees-13.14)
}

// bees-13.11 reads [checks], [goal.<name>] and [workspace.command]: neither bs config nor
// doctor calls them unread, and doctor fails a [checks] argv[0] it can't find.
func TestTheCommandKeysAreRead(t *testing.T) {
	r := newRig(t)
	r.write("projects/mytoy.toml", "[checks.lint]\nrun = [\"bs-no-such-lint\", \"lint\"]\n")
	_, out, _ := r.run(r.repo, nil, "config")
	wantIn(t, out, `checks.lint.run = ["bs-no-such-lint", "lint"]  # project `, "# [workspace.command] unset\n", "# [goal.<name>] unset\n")
	for line := range strings.Lines(out) {
		if strings.Contains(line, "not read by") && (strings.Contains(line, "checks.") || strings.Contains(line, "goal.") || strings.Contains(line, "workspace.command")) {
			t.Errorf("bs config calls a key unread: %s", line)
		}
	}
	_, out, _ = r.run(r.repo, nil, "doctor")
	wantIn(t, out, "FAIL checks.lint.run: bs-no-such-lint is not on")
	if strings.Contains(out, "checks.lint.run yet") || strings.Contains(out, "guards") {
		t.Errorf("doctor calls the guards unread; bees-13.6 reads them:\n%s", out)
	}
	if strings.Contains(out, "model set") || strings.Contains(out, "workspace.command") {
		t.Errorf("doctor warns of a key nothing set:\n%s", out)
	}
}

// A workspace kind the config chooses and bs can't make yet refuses the spawn: it never
// becomes a git_worktree behind the config's back.
func TestSpawnRefusesAKindItCantMake(t *testing.T) {
	r := newRig(t)
	for body, want := range map[string]string{
		"[workspace]\nkind = \"none\"\n":           "bs spawn makes git_worktree and command workspaces, and none only once bees-13.10",
		"[workspace.command]\ncreate = [\"mk\"]\n": "[workspace.command] has no remove, clean, dir",
	} {
		r.write("projects/mytoy.toml", body)
		var out, errb bytes.Buffer
		ports := Ports{Run: port.Exec{}, Harness: noHarness{}, CacheDir: filepath.Join(r.home, "cache")}
		code := Run(context.Background(), Env{Vars: r.env, Dir: r.repo, Self: os.Getpid()}, ports, []string{"spawn", "--as", "queen-mytoy", "--goal", "branch", "mytoy-1"}, &out, &errb)
		if code != 2 {
			t.Errorf("%q: exit %d %s", body, code, errb.String())
		}
		wantIn(t, errb.String(), want)
	}
}

// noHarness is a harness spawn never reaches.
type noHarness struct{ port.Harness }

// Motif 11: a paths.repo that doesn't exist is that, never "can't tell whether it's a git
// checkout" (git can't start in a dir that's gone): config runs and says so, doctor
// FAILs it, spawn refuses (exit 2).
func TestAMissingPathsRepo(t *testing.T) {
	r := newRig(t)
	gone := filepath.Join(filepath.Dir(r.repo), "gone")
	r.write("projects/other.toml", "[paths]\nrepo = \""+gone+"\"\n")
	outside := []string{"BS_PROJECT=other"}
	code, out, stderr := r.run("/", outside, "config")
	if code != 0 {
		t.Errorf("config: exit %d %s", code, stderr)
	}
	wantIn(t, stderr, "warning: the repo "+gone+" (paths.repo, project ")
	wantIn(t, out, "# repo: "+gone+" (paths.repo, project ", "; missing)")
	_, out, _ = r.run("/", outside, "doctor")
	wantIn(t, out, "FAIL repo: "+gone)
	var o, e bytes.Buffer
	ports := Ports{Run: port.Exec{}, Harness: noHarness{}, CacheDir: filepath.Join(r.home, "cache")}
	env := Env{Vars: append(append([]string{}, r.env...), outside...), Dir: "/", Self: os.Getpid()}
	if code := Run(context.Background(), env, ports, []string{"spawn", "--as", "queen-other", "--goal", "branch", "other-1"}, &o, &e); code != 2 {
		t.Errorf("spawn: exit %d %s", code, e.String())
	}
	wantIn(t, e.String(), "the repo "+gone, "doesn't exist", "next: set paths.repo")
}

// A broken file refuses every command but doctor, which names it as a FAIL and runs the
// checks that need no config.
func TestBrokenFileRefusesEveryCommand(t *testing.T) {
	r := newRig(t)
	r.write("config.toml", "[paths]\nlogbook_dir = \""+r.logbook+"\"\nrepo = \"/x\"\n")
	for _, cmd := range []string{"config", "status", "inbox"} {
		code, _, stderr := r.run(r.repo, nil, cmd)
		if code != 2 || !strings.Contains(stderr, "paths.repo belongs in the project file") {
			t.Errorf("%s: exit %d %s", cmd, code, stderr)
		}
	}
	code, out, _ := r.run(r.repo, nil, "doctor")
	if code != 1 {
		t.Errorf("doctor: exit %d", code)
	}
	wantIn(t, out, "FAIL config: "+filepath.Join(r.session, "config.toml")+": paths.repo belongs in the project file", "the checks below run on the defaults", "herdr")
}

// Rule G: an unreadable file is "couldn't look" (exit 6). The only warning load gives
// before it is a recorded pane's BS_* (TestBSVariablesAreIgnoredInsideARecordedPane).
func TestAnUnreadableFileIsExit6(t *testing.T) {
	r := newRig(t)
	r.write("projects/mytoy.toml", "")
	if err := os.Chmod(filepath.Join(r.session, "projects", "mytoy.toml"), 0); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() == 0 {
		t.Skip("root reads a mode-0 file")
	}
	code, _, stderr := r.run(r.repo, nil, "config")
	if code != 6 {
		t.Errorf("exit %d, want 6: %s", code, stderr)
	}
	wantIn(t, stderr, "couldn't read its config")
}

// The project is the repo's main checkout however git keeps it: a separate git dir too.
// A linked worktree whose main checkout git doesn't record (a separate git dir's, a bare
// repo's) and a bare repo name no project: refused (exit 2), not "couldn't look".
func TestTheProjectFromAnyCheckout(t *testing.T) {
	r := newRig(t)
	root := filepath.Dir(r.repo)
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	main := filepath.Join(root, "sep")
	git("init", "-q", "--separate-git-dir", filepath.Join(root, "sep.gitdir"), main)
	git("-C", main, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "x")
	if code, out, stderr := r.run(main, nil, "config"); code != 0 || !strings.Contains(out, "# project: sep (the git repo at "+main+")") {
		t.Errorf("a separate git dir: exit %d %s %s", code, out, stderr)
	}
	bare := filepath.Join(root, "bare.git")
	git("-C", main, "worktree", "add", "-q", filepath.Join(root, "sep-wt"))
	git("clone", "-q", "--bare", main, bare)
	git("-C", bare, "worktree", "add", "-q", filepath.Join(root, "bare-wt"))
	for dir, want := range map[string]string{filepath.Join(root, "sep-wt"): "whose main checkout git doesn't record",
		filepath.Join(root, "bare-wt"): "whose main checkout git doesn't record", bare: "a bare repo"} {
		if code, _, stderr := r.run(dir, nil, "config"); code != 2 || !strings.Contains(stderr, want) {
			t.Errorf("%s: exit %d %s", dir, code, stderr)
		}
	}
}

// gitless runs every command as if git weren't installed.
type gitless struct{}

func (gitless) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, exec.ErrNotFound
}

// Rule G: a git that can't run is "couldn't look" (exit 6), never "no project".
func TestAFailingGitIsNotNoProject(t *testing.T) {
	r := newRig(t)
	var out, errb bytes.Buffer
	code := Run(context.Background(), Env{Vars: r.env, Dir: r.repo, Self: os.Getpid()}, Ports{Run: gitless{}}, []string{"config"}, &out, &errb)
	if code != 6 {
		t.Errorf("exit %d, want 6: %s", code, errb.String())
	}
	wantIn(t, errb.String(), "can't tell which project")
	// Outside any repo is an answer: config runs on the global file.
	if code, out, stderr := r.run(t.TempDir(), nil, "config"); code != 0 || !strings.Contains(out, "# project: none") {
		t.Errorf("outside a repo: exit %d %s %s", code, out, stderr)
	}
}

// bs config init writes the session's global file, then a project's, each loading as
// written; it refuses what bs would refuse of the file, and never overwrites one.
func TestConfigInit(t *testing.T) {
	r := newRig(t)
	if err := os.RemoveAll(r.session); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(r.session, "config.toml")
	code, _, stderr := r.run(r.repo, nil, "config", "init")
	if code != 2 {
		t.Errorf("on the real logbook dir: exit %d", code)
	}
	wantIn(t, stderr, "paths.logbook_dir would be the real logbooks' dir", "next: bs config init --logbook-dir")
	if _, err := os.Stat(global); !os.IsNotExist(err) {
		t.Fatalf("a refused init wrote %s: %v", global, err)
	}
	code, out, stderr := r.run(r.repo, nil, "config", "init", "--logbook-dir", r.logbook, "--harness", "claude_code")
	if code != 0 {
		t.Fatalf("exit %d %s", code, stderr)
	}
	wantIn(t, out, "wrote "+global)
	code, out, _ = r.run(r.repo, nil, "config")
	if code != 0 {
		t.Fatalf("config: exit %d", code)
	}
	wantIn(t, out, `paths.logbook_dir = "`+r.logbook+`"  # global `+global, `harness = "claude_code"  # global `+global, `review.host = "github"  # default`)
	if code, _, stderr = r.run(r.repo, nil, "config", "init", "--logbook-dir", r.logbook); code != 2 || !strings.Contains(stderr, global+" already exists; nothing was written") {
		t.Errorf("a second init: exit %d %s", code, stderr)
	}
	for _, args := range [][]string{{"--project", "--harness", "claude_code"}, {"--project", "--repo", "/nonexistent/notes"}, {"--project", "--workspace", "jj"}} {
		code, _, stderr := r.run(r.repo, nil, append([]string{"config", "init"}, args...)...)
		if code != 2 {
			t.Errorf("%v: exit %d %s", args, code, stderr)
		}
	}
	// A kind spawn won't make yet is never written (bees-13.10), nor a command kind with
	// no table to make it from.
	for kind, want := range map[string]string{"none": "once bees-13.10 brings it", "command": "which config init doesn't write"} {
		if code, _, stderr := r.run(r.repo, nil, "config", "init", "--project", "--workspace", kind); code != 2 || !strings.Contains(stderr, want) {
			t.Errorf("--workspace %s: exit %d %s", kind, code, stderr)
		}
	}
	code, out, stderr = r.run(r.repo, nil, "config", "init", "--project", "--workspace", "git_worktree", "--repo", r.repo)
	if code != 0 {
		t.Fatalf("--project: exit %d %s", code, stderr)
	}
	project := filepath.Join(r.session, "projects", "mytoy.toml")
	wantIn(t, out, "wrote "+project)
	_, out, _ = r.run(r.repo, nil, "config")
	wantIn(t, out, `workspace.kind = "git_worktree"  # project `+project, "# workspace kind: git_worktree (workspace.kind, project "+project+")")
	b, _ := os.ReadFile(project)
	wantIn(t, string(b), "# path = []", `repo = "`+r.repo+`"`)
}

// Rule G: config init that can't look (the global file it reads over, --repo) refuses
// with exit 6 and writes nothing, never reading "can't look" as "absent".
func TestConfigInitCantLook(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a mode-0 file")
	}
	r := newRig(t)
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(filepath.Join(locked, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	code, _, stderr := r.run(r.repo, nil, "config", "init", "--project", "--repo", filepath.Join(locked, "notes"))
	if code != 6 || !strings.Contains(stderr, "can't look at --repo") {
		t.Errorf("--repo under a dir it can't search: exit %d %s", code, stderr)
	}
	// load reads the global file first, so initRefusals is called directly.
	global := filepath.Join(r.session, "config.toml")
	if err := os.Chmod(global, 0); err != nil {
		t.Fatal(err)
	}
	run := &run{ctx: context.Background(), env: Env{Vars: r.env}, s: &settings{Dir: r.session, Project: "mytoy"}}
	if err := run.initRefusals("project", nil, ""); !errors.Is(err, os.ErrPermission) {
		t.Errorf("an unreadable global file: %v, want permission denied", err)
	}
}

// A moved paths.logbook_dir never starts an empty logbook while the old dir holds the
// project's one (bees-13.73.5, motif 14): bs queen and bs spawn are refused too.
func TestRefusesAMovedLogbookDir(t *testing.T) {
	r := newRig(t)
	old := filepath.Join(r.home, ".local", "share", "bees")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "mytoy.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range [][]string{{"queen"}, {"spawn", "--goal", "pr", "mytoy-1"}, {"status"}} {
		code, _, stderr := r.run(r.repo, nil, cmd...)
		if code != core.ExitRefused {
			t.Errorf("bs %s: exit %d, want %d: %s", cmd, code, core.ExitRefused, stderr)
		}
		wantIn(t, stderr, "paths.logbook_dir moved there", "from ~/.local/share/bees", "move "+filepath.Join(old, "mytoy.db")+" into "+r.logbook)
	}
	if _, err := os.Stat(filepath.Join(r.logbook, "mytoy.db")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a logbook was made at the new dir: %v", err)
	}
	r.start() // the new dir has one: the old one no longer matters
	if code, _, stderr := r.run(r.repo, nil, "status"); code != 0 {
		t.Errorf("bs status with both: exit %d: %s", code, stderr)
	}
}

// bees-14.1: bs queen on harness agy looks for agy.bin, ~ expanded, not agy on PATH.
func TestQueenLooksForAgyBin(t *testing.T) {
	r := newRig(t)
	r.write("config.toml", "harness = \"agy\"\n[agy]\nbin = \"~/opt/agy\"\n[paths]\nlogbook_dir = \""+r.logbook+"\"\n")
	var out, errb bytes.Buffer
	ports := Ports{Run: port.Exec{}, CacheDir: filepath.Join(r.home, "cache"), Harness: agy.Adapter{Bs: "/bin/bs"}}
	code := Run(context.Background(), Env{Vars: append(append([]string{}, r.env...), "HERDR_PANE_ID=w1:p1"), Dir: r.repo, Self: os.Getpid()}, ports, []string{"queen"}, &out, &errb)
	if code != 2 {
		t.Errorf("exit %d", code)
	}
	wantIn(t, errb.String(), filepath.Join(r.home, "opt", "agy")+" is missing or not executable (agy.bin)")
}
