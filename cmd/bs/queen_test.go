package main

import (
	"os"
	"path/filepath"

	"github.com/jmmshn/bees/internal/prompt"
	"slices"
	"strings"
	"testing"
)

func TestQueenStartsClaudeNamedForTheProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BS_PROJECT", "bees")
	t.Setenv("BS_LOGBOOK", "")
	t.Setenv("BS_LOGBOOK_DIR", "")
	t.Setenv("BS_REPOS", "/src")
	t.Setenv("BS_SESSION", "stale")
	t.Chdir(t.TempDir()) // not this repo: its own checkout would beat BS_REPOS
	var gotDir string
	var gotEnv, gotArgs []string
	execClaude = func(dir string, env []string, args ...string) error {
		gotDir, gotEnv, gotArgs = dir, env, args
		return nil
	}
	t.Cleanup(func() { execClaude = execClaudeProcess })

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
	t.Setenv("BS_REPOS", "")
	t.Chdir(t.TempDir())
	execClaude = func(string, []string, ...string) error { t.Fatal("started Claude in no repo"); return nil }
	t.Cleanup(func() { execClaude = execClaudeProcess })

	if _, stderr, code := bs(t, "queen"); code != 1 || !strings.Contains(stderr, "set BS_REPOS") {
		t.Fatalf("queen: exit %d, stderr %q; want exit 1 telling the keeper to set BS_REPOS", code, stderr)
	}
}

// bees-11.4: [queen.claude_code] model is passed as --model; empty passes none.
func TestQueenStartsOnTheConfiguredModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("BS_PROJECT", "bees")
	t.Setenv("BS_LOGBOOK", "")
	t.Setenv("BS_LOGBOOK_DIR", "")
	t.Setenv("BS_REPOS", "/src")
	t.Chdir(t.TempDir())
	var gotArgs []string
	execClaude = func(_ string, _ []string, args ...string) error { gotArgs = args; return nil }
	t.Cleanup(func() { execClaude = execClaudeProcess })

	if _, stderr, code := bs(t, "queen"); code != 0 {
		t.Fatalf("queen: exit %d: %s", code, stderr)
	}
	if slices.ContainsFunc(gotArgs, func(a string) bool { return strings.HasPrefix(a, "--model") }) {
		t.Errorf("args = %q, want no --model when none is configured", gotArgs)
	}
	writeModels(t, xdg, "bees")
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
