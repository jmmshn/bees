package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
)

// fakeBins puts a script for each bin on a PATH of its own, printing its output for its
// first argument, as the real tool does (motif 13). Outputs hold no single quote.
func fakeBins(t *testing.T, bins map[string]map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, outs := range bins {
		script := "#!/bin/sh\ncase \"$1\" in\n" // printf is a builtin: nothing else is on PATH
		for arg, out := range outs {
			script += arg + ") printf '%s\\n' '" + out + "' ;;\n"
		}
		script += "*) exit 1 ;;\nesac\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// Real outputs: herdr 0.9.5, Claude Code 2.1.306, bd 1.1.0.
var (
	herdrUp     = map[string]string{"--version": "herdr 0.9.5", "status": "client:\n  version: 0.9.5\n  channel: stable\n\nserver:\n  status: running\n  version: 0.9.5\n  endpoint_compatible: yes"}
	claudeOK    = map[string]string{"--version": "2.1.306 (Claude Code)"}
	bdOK        = map[string]string{"--version": "bd version 1.1.2 (dev)", "where": "/home/x/.local/share/beads/.beads\n  database: /home/x/.local/share/beads/.beads/dolt"}
	goodMachine = map[string]map[string]string{"herdr": herdrUp, "claude": claudeOK, "bd": bdOK}
)

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
	fakeBins(t, goodMachine)
	var out, stderr bytes.Buffer
	code := run([]string{"doctor"}, strings.NewReader(""), &out, &stderr)
	if code != 0 {
		t.Fatalf("exit %d:\n%s%s", code, out.String(), stderr.String())
	}
	for _, want := range []string{
		"ok    config: ",
		"ok    harness claude_code: claude 2.1.306",
		"ok    herdr: 0.9.5, server 0.9.5 running",
		"ok    herdr pane: this shell runs in w1:p1",
		"ok    logbook dir: ",
		"ok    bd: 1.1.2, store /home/x/.local/share/beads/.beads\n",
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
	fakeBins(t, map[string]map[string]string{
		"herdr":  {"--version": "herdr 0.9.5", "status": "client:\n  version: 0.9.5\n\nserver:\n  status: not running"},
		"claude": {"--version": "2.1.100 (Claude Code)"},
		"bd":     {"--version": "bd version 0.9.0"},
	})
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
	fakeBins(t, map[string]map[string]string{"herdr": herdrUp, "agy": {"--version": "1.4.4"}, "bd": bdOK})
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
	fakeBins(t, goodMachine)
	proj := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bees", "projects", "toy.toml")
	if err := os.MkdirAll(filepath.Dir(proj), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proj, []byte("workspace.kind = \"none\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
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

// T3: a ring file that isn't 0600 fails, and so does a herdr whose server speaks another
// protocol.
func TestDoctorRingFileAndHerdrEndpoint(t *testing.T) {
	doctorEnv(t)
	ring := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "bees")
	if err := os.Mkdir(ring, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ring, "w.token"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fakeBins(t, map[string]map[string]string{
		"herdr":  {"--version": "herdr 0.9.5", "status": "client:\n  version: 0.9.5\n\nserver:\n  status: running\n  version: 0.8.0\n  endpoint_compatible: no"},
		"claude": claudeOK, "bd": bdOK,
	})
	var out, stderr bytes.Buffer
	if code := run([]string{"doctor"}, strings.NewReader(""), &out, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1:\n%s%s", code, out.String(), stderr.String())
	}
	for _, want := range []string{
		"FAIL  ring sockets: " + filepath.Join(ring, "w.token") + " is mode 0644, not 0600",
		"FAIL  herdr: client 0.9.5 can't talk to server 0.8.0",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// bees-12.15: doctor names a live session on another harness, which bs queen and bs spawn
// refuse to start beside.
func TestDoctorHarnessSwitch(t *testing.T) {
	doctorEnv(t)
	fakeBins(t, goodMachine)
	ctx := context.Background()
	lb, err := logbook.Open(ctx, filepath.Join(os.Getenv("BS_LOGBOOK_DIR"), "toy.db"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := lb.RecordSpawn(ctx, "queen-toy", logbook.Spawn{Name: "toy-1-9", Bead: "toy-1.9", Harness: "agy", Pane: "w9:p1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	start, _ := proc.Start(os.Getpid()) // this test's own process: running
	if _, err := lb.RecordStart(ctx, logbook.Start{Spawn: id, Name: "toy-1-9", Process: proc.Process{PID: os.Getpid(), Start: start}}); err != nil {
		t.Fatal(err)
	}
	lb.Close()
	var out, stderr bytes.Buffer
	if code := run([]string{"doctor", "--project", "toy"}, strings.NewReader(""), &out, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1:\n%s%s", code, out.String(), stderr.String())
	}
	if want := "FAIL  harness switch: toy-1-9 (agy) still runs"; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out.String())
	}
}

// bees-12.44: a herdr server older than its client still answers, but bs's probes ran on
// the client's version, so doctor warns rather than passing it.
func TestDoctorWarnsOnAnOlderHerdrServer(t *testing.T) {
	doctorEnv(t)
	fakeBins(t, map[string]map[string]string{
		"herdr":  {"--version": "herdr 0.9.5", "status": "client:\n  version: 0.9.5\n\nserver:\n  status: running\n  version: 0.9.3\n  endpoint_compatible: yes"},
		"claude": claudeOK, "bd": bdOK,
	})
	var out, stderr bytes.Buffer
	if code := run([]string{"doctor"}, strings.NewReader(""), &out, &stderr); code != 0 {
		t.Fatalf("exit %d, want 0 (a warn):\n%s%s", code, out.String(), stderr.String())
	}
	if want := "warn  herdr: 0.9.5, but server 0.9.3 is older"; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out.String())
	}
}

// bees-12.44: under github_pr, a repo with no git remote passes every other check, then
// fails at a worker's first push; doctor warns, and stops once a remote exists.
func TestDoctorWarnsOnGitHubPRWithNoRemote(t *testing.T) {
	doctorEnv(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	repo := filepath.Join(t.TempDir(), "toy")
	if out, err := exec.Command(git, "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	t.Chdir(repo)
	fakeBins(t, goodMachine)
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+filepath.Dir(git))
	want := "warn  git remote: none in "
	var out, stderr bytes.Buffer
	run([]string{"doctor"}, strings.NewReader(""), &out, &stderr)
	if !strings.Contains(out.String(), want) {
		t.Errorf("no remote: output lacks %q:\n%s%s", want, out.String(), stderr.String())
	}
	if out, err := exec.Command(git, "-C", repo, "remote", "add", "origin", "https://github.com/o/toy").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v: %s", err, out)
	}
	out.Reset()
	run([]string{"doctor"}, strings.NewReader(""), &out, &stderr)
	if strings.Contains(out.String(), want) {
		t.Errorf("with origin: output still has %q:\n%s", want, out.String())
	}
}
