package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmmshn/bees/cmd/bsx/internal/port"
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
	root := t.TempDir()
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

// run runs bsx args in dir with env plus extra, and returns its exit code and output.
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

// bsx never opens the real logbooks before the swap: not through ~/.local/share/bees,
// $XDG_DATA_HOME/bees, or a symlink to either; nor runs on the default session's dir.
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
		if code != 2 || !strings.Contains(stderr, "refuses the default config dir") {
			t.Errorf("logbook_dir %s: exit %d %s", d, code, stderr)
		}
	}
	r.write("config.toml", "[paths]\nlogbook_dir = \""+r.logbook+"\"\n")
	if code, _, stderr := r.run(r.repo, nil, "status"); code != 0 {
		t.Fatalf("a toy's own logbook dir: exit %d %s", code, stderr)
	}
	for _, s := range []string{"HERDR_SESSION=", "HERDR_SESSION=default"} {
		if code, _, stderr := r.run(r.repo, []string{s}, "config"); code != 2 || !strings.Contains(stderr, "refuses the default config dir") {
			t.Errorf("%s: exit %d %s", s, code, stderr)
		}
	}
}

// Rule G: with the session's config dir missing, every writing command refuses (exit 2)
// and writes nothing; status, config and doctor run, warning each call.
func TestMissingSessionDir(t *testing.T) {
	r := newRig(t)
	if err := os.RemoveAll(r.session); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"spawn", "--goal", "branch", "toy-1"}, {"send", "--as", "x", "y", "hi"}, {"inbox", "--as", "x"},
		{"end", "--as", "queen-toy", "toy-1"}, {"prune", "--as", "queen-toy"}, {"done", "--as", "toy-1"}} {
		code, out, stderr := r.run(r.repo, nil, args...)
		if code != 2 || out != "" {
			t.Errorf("%v: exit %d, out %q", args, code, out)
		}
		wantIn(t, stderr, "warning: no config dir for this herdr session toy", "refused", "nothing was written")
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
	// status runs, warns, and then meets the pre-swap refusal: the default logbook dir is
	// the real one.
	code, _, stderr = r.run(r.repo, nil, "status")
	if code != 2 {
		t.Errorf("status: exit %d", code)
	}
	wantIn(t, stderr, "warning: no config dir", "refuses the default config dir")
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
	code, out, _ := r.run("/", []string{"BS_PROJECT=other"}, "config")
	if code != 0 {
		t.Errorf("outside any repo: exit %d", code)
	}
	wantIn(t, out, "# project: other (env BS_PROJECT)")
}

func TestBrokenFileRefusesEveryCommand(t *testing.T) {
	r := newRig(t)
	r.write("config.toml", "[paths]\nlogbook_dir = \""+r.logbook+"\"\nrepo = \"/x\"\n")
	for _, cmd := range []string{"config", "status", "doctor"} {
		code, _, stderr := r.run(r.repo, nil, cmd)
		if code != 2 || !strings.Contains(stderr, "paths.repo belongs in the project file") {
			t.Errorf("%s: exit %d %s", cmd, code, stderr)
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
