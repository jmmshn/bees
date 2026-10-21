package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
	"github.com/jmmshn/bees/internal/prompt"
)

func TestQueenStartsClaudeNamedForTheProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BS_PROJECT", "bees")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("BS_LOGBOOK", "")
	t.Setenv("BS_LOGBOOK_DIR", "")
	t.Setenv("BS_REPOS", "/src")
	t.Setenv("BS_SESSION", "stale")
	t.Chdir(t.TempDir()) // not this repo: its own checkout would beat BS_REPOS
	var gotDir string
	var gotEnv, gotArgs []string
	execAgent = func(dir string, env []string, _ string, args ...string) error {
		gotDir, gotEnv, gotArgs = dir, env, args
		return nil
	}
	t.Cleanup(func() { execAgent = execAgentProcess })

	_, stderr, code := bs(t, "queen")
	if code != 0 {
		t.Fatalf("queen: exit %d: %s", code, stderr)
	}
	// The queen starts the project: her logbook exists before her first bs command.
	if !strings.Contains(stderr, "bs: new empty logbook for project bees") {
		t.Errorf("stderr = %q; want the new logbook announced", stderr)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".local/share/bees/bees.db")); err != nil {
		t.Errorf("queen made no logbook: %v", err)
	}
	if gotDir != "/src/bees" {
		t.Errorf("dir = %q", gotDir)
	}
	// A file, as for a worker (herdr can't pass multi-line arguments), but the queen's
	// own role prompt.
	if len(gotArgs) != 4 || !slices.Equal(gotArgs[:3], []string{"--name", "queen-bees", "--append-system-prompt-file"}) {
		t.Fatalf("args = %q", gotArgs)
	}
	if b, err := os.ReadFile(gotArgs[3]); err != nil || string(b) != prompt.Text(prompt.Queen, "bees") {
		t.Errorf("system prompt file %s: %v\n%s", gotArgs[3], err, b)
	}
	for _, kv := range gotEnv {
		if strings.HasPrefix(kv, "BS_SESSION=") || strings.HasPrefix(kv, "BS_PROJECT=") {
			t.Errorf("queen env sets %s; identity must come from --name and the repo", kv)
		}
	}
}

func TestQueenOutsideItsRepoWantsBSRepos(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BS_PROJECT", "bees")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("BS_REPOS", "")
	t.Chdir(t.TempDir())
	execAgent = func(string, []string, string, ...string) error { t.Fatal("started Claude in no repo"); return nil }
	t.Cleanup(func() { execAgent = execAgentProcess })

	if _, stderr, code := bs(t, "queen"); code != 1 || !strings.Contains(stderr, "set BS_REPOS") {
		t.Fatalf("queen: exit %d, stderr %q; want exit 1 telling the keeper to set BS_REPOS", code, stderr)
	}
}

// A relative BS_LOGBOOK reaches the queen absolute: she runs in her repo, where the same
// relative path would name another file and split the store (bees-11.3 review).
func TestQueenGetsAnAbsoluteLogbook(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BS_PROJECT", "bees")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("BS_REPOS", "/src")
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("BS_LOGBOOK", "bees.db")
	var gotEnv []string
	execAgent = func(_ string, env []string, _ string, _ ...string) error { gotEnv = env; return nil }
	t.Cleanup(func() { execAgent = execAgentProcess })
	if _, stderr, code := bs(t, "queen"); code != 0 {
		t.Fatalf("queen: exit %d: %s", code, stderr)
	}
	want, _ := filepath.Abs("bees.db")
	if !slices.Contains(gotEnv, "BS_LOGBOOK="+want) || slices.Contains(gotEnv, "BS_LOGBOOK=bees.db") {
		t.Errorf("queen env BS_LOGBOOK = %q, want %s", slices.DeleteFunc(gotEnv, func(kv string) bool { return !strings.HasPrefix(kv, "BS_LOGBOOK=") }), want)
	}
}

// bees-11.4: [queen.claude_code] model is passed as --model; empty passes none.
func TestQueenStartsOnTheConfiguredModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("BS_PROJECT", "bees")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("BS_LOGBOOK", "")
	t.Setenv("BS_LOGBOOK_DIR", "")
	t.Setenv("BS_REPOS", "/src")
	t.Chdir(t.TempDir())
	var gotArgs []string
	execAgent = func(_ string, _ []string, _ string, args ...string) error { gotArgs = args; return nil }
	t.Cleanup(func() { execAgent = execAgentProcess })

	if _, stderr, code := bs(t, "queen"); code != 0 {
		t.Fatalf("queen: exit %d: %s", code, stderr)
	}
	if slices.ContainsFunc(gotArgs, func(a string) bool { return strings.HasPrefix(a, "--model") }) {
		t.Errorf("args = %q, want no --model when none is configured", gotArgs)
	}
	writeModels(t, xdg, "bees")
	t.Setenv("HOME", t.TempDir()) // a new logbook: the stubbed exec left the first queen "running"
	if _, stderr, code := bs(t, "queen"); code != 0 {
		t.Fatalf("queen: exit %d: %s", code, stderr)
	}
	if !slices.Contains(gotArgs, "--model=claude-opus-5-5") {
		t.Errorf("args = %q, want --model=claude-opus-5-5 (the queen's, not the workers')", gotArgs)
	}
}

// writeModels writes project's config file under xdg with a model for each role: the
// queen's claude-opus-5-5, the workers' claude-haiku-5-5.
func writeModels(t *testing.T, xdg, project string) {
	t.Helper()
	cfg := filepath.Join(xdg, "bees", "projects", project+".toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[queen.claude_code]\nmodel = \"claude-opus-5-5\"\n[worker.claude_code]\nmodel = \"claude-haiku-5-5\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bees-12.10: bs queen records her pane and process before it execs, so her workers can
// find her; exec keeps the pid and start time.
func TestQueenRecordsHerPaneAndProcess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BS_PROJECT", "bees")
	t.Setenv("BS_LOGBOOK", "")
	t.Setenv("BS_LOGBOOK_DIR", "")
	t.Setenv("BS_REPOS", "/src")
	t.Setenv("HERDR_PANE_ID", "w7:p2")
	t.Chdir(t.TempDir())
	execAgent = func(string, []string, string, ...string) error { return nil }
	t.Cleanup(func() { execAgent = execAgentProcess })
	if _, stderr, code := bs(t, "queen"); code != 0 {
		t.Fatalf("queen: exit %d: %s", code, stderr)
	}
	lb, err := logbook.Open(context.Background(), filepath.Join(os.Getenv("HOME"), ".local/share/bees/bees.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	q, ok, err := lb.LatestQueen(context.Background(), "queen-bees")
	start, _ := proc.Start(os.Getpid())
	if err != nil || !ok || q.Pane != "w7:p2" || q.PID != os.Getpid() || q.Start != start || q.Harness != "claude_code" {
		t.Fatalf("queen entry = %+v, %v, %v; want pane w7:p2, this pid and start, claude_code", q, ok, err)
	}
	// This process is still alive with that start time: a second queen is refused.
	execAgent = func(string, []string, string, ...string) error { t.Fatal("started a twin queen"); return nil }
	if _, stderr, code := bs(t, "queen"); code != 2 || !strings.Contains(stderr, "already running") {
		t.Fatalf("twin queen: exit %d, stderr %q; want exit 2", code, stderr)
	}
}

// bees-12.10: a queen whose recorded process is gone doesn't block the next one.
func TestQueenStartsOverADeadQueen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BS_PROJECT", "bees")
	t.Setenv("BS_LOGBOOK", "")
	t.Setenv("BS_LOGBOOK_DIR", "")
	t.Setenv("BS_REPOS", "/src")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Chdir(t.TempDir())
	lb, err := logbook.Open(context.Background(), filepath.Join(os.Getenv("HOME"), ".local/share/bees/bees.db"))
	if err != nil {
		t.Fatal(err)
	}
	start, _ := proc.Start(os.Getpid())
	if _, err := lb.RecordQueen(context.Background(), logbook.Queen{Name: "queen-bees", Harness: "claude_code", Pane: "w1:p1", PID: os.Getpid(), Start: start + "0"}); err != nil {
		t.Fatal(err)
	}
	lb.Close()
	started := false
	execAgent = func(string, []string, string, ...string) error { started = true; return nil }
	t.Cleanup(func() { execAgent = execAgentProcess })
	if _, stderr, code := bs(t, "queen"); code != 0 || !started {
		t.Fatalf("queen over a dead one (reused pid): exit %d, started %v: %s", code, started, stderr)
	}
}

// bees-12.10: outside a herdr pane bs queen refuses with exit 2 and records nothing.
func TestQueenRefusesOutsideHerdr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BS_PROJECT", "bees")
	t.Setenv("BS_REPOS", "/src")
	t.Setenv("HERDR_PANE_ID", "")
	t.Chdir(t.TempDir())
	execAgent = func(string, []string, string, ...string) error { t.Fatal("started a queen outside herdr"); return nil }
	t.Cleanup(func() { execAgent = execAgentProcess })
	if _, stderr, code := bs(t, "queen"); code != 2 || !strings.Contains(stderr, "not in a herdr pane") {
		t.Fatalf("exit %d, stderr %q; want exit 2", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".local/share/bees/bees.db")); !os.IsNotExist(err) {
		t.Fatalf("logbook made outside herdr: %v", err)
	}
}
