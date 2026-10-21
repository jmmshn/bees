package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBins puts scripts named after each key on a PATH of their own; each prints its
// value, which holds no single quote.
func fakeBins(t *testing.T, bins map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, out := range bins {
		script := "#!/bin/sh\nprintf '%s\\n' '" + out + "'\n" // printf is a builtin: nothing else is on PATH
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

const herdrUp = "herdr 0.9.5\nclient:\n  version: 0.9.5\nserver:\n  status: running\n  version: 0.9.3"

func doctorEnv(t *testing.T) {
	t.Helper()
	configEnv(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("BS_LOGBOOK_DIR", t.TempDir())
	t.Chdir(t.TempDir()) // no project: its checks are skipped
}

// bees-12.9: a machine with everything prints one ok line per check and exits 0.
func TestDoctorAllGood(t *testing.T) {
	doctorEnv(t)
	fakeBins(t, map[string]string{"herdr": herdrUp, "claude": "2.1.306 (Claude Code)", "bd": "bd version 1.1.2 (dev)\n/home/x/.beads"})
	var out, stderr bytes.Buffer
	code := run([]string{"doctor"}, strings.NewReader(""), &out, &stderr)
	if code != 0 {
		t.Fatalf("exit %d:\n%s%s", code, out.String(), stderr.String())
	}
	for _, want := range []string{
		"ok    config: ",
		"ok    harness claude_code: claude 2.1.306",
		"ok    herdr: 0.9.5, server 0.9.3 running",
		"ok    herdr pane: w1:p1",
		"ok    logbook dir: ",
		"ok    bd: 1.1.2",
		"ok    ring sockets: ",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !strings.HasPrefix(l, "ok    ") && !strings.HasPrefix(l, "warn  ") {
			t.Errorf("not a check line: %q", l)
		}
	}
}

// Each problem is its own FAIL line, and any FAIL exits 1.
func TestDoctorFailures(t *testing.T) {
	doctorEnv(t)
	t.Setenv("HERDR_PANE_ID", "")
	ring := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "bees")
	if err := os.Mkdir(ring, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBins(t, map[string]string{"herdr": "herdr 0.9.5\nserver:\n  status: not running", "claude": "2.1.100 (Claude Code)", "bd": "bd version 0.9.0"})
	var out, stderr bytes.Buffer
	code := run([]string{"doctor"}, strings.NewReader(""), &out, &stderr)
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s%s", code, out.String(), stderr.String())
	}
	for _, want := range []string{
		"FAIL  harness claude_code: claude 2.1.100 is older than 2.1.296",
		"FAIL  herdr: server not running",
		"warn  herdr pane: ",
		"FAIL  bd: bd 0.9.0 is older than 1.1.0",
		"FAIL  ring sockets: " + ring + " is mode 0755, not 0700",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// Under agy, doctor says why bs refuses to start it.
func TestDoctorAgyStaged(t *testing.T) {
	doctorEnv(t)
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bees")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("harness = \"agy\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeBins(t, map[string]string{"herdr": herdrUp, "agy": "agy 1.4.4", "bd": "bd version 1.1.2"})
	var out, stderr bytes.Buffer
	if code := run([]string{"doctor"}, strings.NewReader(""), &out, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1:\n%s%s", code, out.String(), stderr.String())
	}
	if !strings.Contains(out.String(), "FAIL  harness agy: agy 1.4.4; harness agy is staged") {
		t.Errorf("no staged refusal:\n%s", out.String())
	}
}

// In a project, doctor fails what bs queen and bs spawn would refuse: workspace none
// with no paths.repo, and none under github_pr.
func TestDoctorProjectRefusals(t *testing.T) {
	doctorEnv(t)
	fakeBins(t, map[string]string{"herdr": herdrUp, "claude": "2.1.306 (Claude Code)", "bd": "bd version 1.1.2"})
	var out, stderr bytes.Buffer
	if code := run([]string{"config", "init", "--project", "toy", "--workspace", "none"}, strings.NewReader(""), &out, &stderr); code != 0 {
		t.Fatalf("config init: exit %d: %s", code, stderr.String())
	}
	out.Reset()
	if code := run([]string{"doctor", "--project", "toy"}, strings.NewReader(""), &out, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1:\n%s%s", code, out.String(), stderr.String())
	}
	for _, want := range []string{
		"ok    project toy: no logbook yet: bs queen makes ",
		`FAIL  repo: workspace.kind = "none" shares paths.repo, which is unset`,
		`FAIL  workspace: workspace.kind = "none" gives a worker no branch`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
