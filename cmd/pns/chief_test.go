package main

import (
	"slices"
	"strings"
	"testing"
)

func TestChiefStartsClaudeNamedForTheProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PNS_PROJECT", "peons")
	t.Setenv("PNS_REPOS", "/src")
	t.Setenv("PNS_SESSION", "stale")
	var gotDir string
	var gotEnv, gotArgs []string
	execClaude = func(dir string, env []string, args ...string) error {
		gotDir, gotEnv, gotArgs = dir, env, args
		return nil
	}
	t.Cleanup(func() { execClaude = execClaudeProcess })

	if _, stderr, code := pns(t, "chief"); code != 0 {
		t.Fatalf("chief: exit %d: %s", code, stderr)
	}
	if gotDir != "/src/peons" {
		t.Errorf("dir = %q", gotDir)
	}
	if !slices.Equal(gotArgs, []string{"--name", "chief-peons"}) {
		t.Errorf("args = %q", gotArgs)
	}
	for _, kv := range gotEnv {
		if strings.HasPrefix(kv, "PNS_SESSION=") || strings.HasPrefix(kv, "PNS_PROJECT=") {
			t.Errorf("chief env sets %s; identity must come from --name and the repo", kv)
		}
	}
}
