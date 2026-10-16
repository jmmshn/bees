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
	// The same file as a bee's: herdr can't pass multi-line arguments, and queen and
	// bees launch the same way.
	if len(gotArgs) != 4 || !slices.Equal(gotArgs[:3], []string{"--name", "queen-bees", "--append-system-prompt-file"}) {
		t.Fatalf("args = %q", gotArgs)
	}
	if b, err := os.ReadFile(gotArgs[3]); err != nil || string(b) != prompt.System("bees") {
		t.Errorf("system prompt file %s: %v\n%s", gotArgs[3], err, b)
	}
	for _, kv := range gotEnv {
		if strings.HasPrefix(kv, "BS_SESSION=") || strings.HasPrefix(kv, "BS_PROJECT=") {
			t.Errorf("queen env sets %s; identity must come from --name and the repo", kv)
		}
	}
}
