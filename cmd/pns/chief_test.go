package main

import (
	"slices"
	"testing"
)

func TestChiefStartsClaudeNamedForTheProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PNS_PROJECT", "peons")
	t.Setenv("PNS_REPOS", "/src")
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
	for _, want := range []string{"PNS_PROJECT=peons", "PNS_SESSION=chief-peons"} {
		if !slices.Contains(gotEnv, want) {
			t.Errorf("env missing %s", want)
		}
	}
}
