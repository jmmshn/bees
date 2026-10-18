package main

import (
	"os"

	"github.com/jmmshn/bees/internal/prompt"
	"slices"
	"strings"
	"testing"
)

func TestQueenStartsClaudeNamedForTheProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BS_PROJECT", "bees")
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

	if _, stderr, code := bs(t, "queen"); code != 0 {
		t.Fatalf("queen: exit %d: %s", code, stderr)
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
	t.Setenv("BS_PROJECT", "bees")
	t.Setenv("BS_REPOS", "")
	t.Chdir(t.TempDir())
	execClaude = func(string, []string, ...string) error { t.Fatal("started Claude in no repo"); return nil }
	t.Cleanup(func() { execClaude = execClaudeProcess })

	if _, stderr, code := bs(t, "queen"); code != 1 || !strings.Contains(stderr, "set BS_REPOS") {
		t.Fatalf("queen: exit %d, stderr %q; want exit 1 telling the keeper to set BS_REPOS", code, stderr)
	}
}
