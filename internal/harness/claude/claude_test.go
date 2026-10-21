package claude_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/harness/claude"
	"github.com/jmmshn/bees/internal/harness/claude/claudetest"
)

func TestRingSendsAuthThenUserLineThenEOF(t *testing.T) {
	reg := claudetest.FakeRegistry(t)
	got := reg.Add(t, "worker-a", "tok123")
	s, err := claude.AtPID(os.Getpid(), "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := claude.Ring(s, "hello", harness.Now); err != nil {
		t.Fatal(err)
	}
	lines := got()
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want auth + user: %v", len(lines), lines)
	}
	if lines[0]["type"] != "auth" || lines[0]["token"] != "tok123" {
		t.Fatalf("auth line = %v", lines[0])
	}
	u := lines[1]
	msg, _ := u["message"].(map[string]any)
	if u["type"] != "user" || u["priority"] != "now" || msg["role"] != "user" || msg["content"] != "hello" || len(u["msg_id"].(string)) != 36 { // msg_id is a UUID
		t.Fatalf("user line = %v", u)
	}
}

func TestRingWithoutTokenSendsNoAuthLine(t *testing.T) {
	reg := claudetest.FakeRegistry(t)
	got := reg.Add(t, "worker-a", "")
	s, err := claude.AtPID(os.Getpid(), "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := claude.Ring(s, "hello", harness.Next); err != nil {
		t.Fatal(err)
	}
	lines := got()
	if len(lines) != 1 || lines[0]["type"] != "user" || lines[0]["priority"] != "next" {
		t.Fatalf("lines = %v, want one user line with priority next", lines)
	}
}

// T4 and SEC-2: AtPID reads only the recorded pid's entry and fails closed on a reused pid
// (procStart differs), another name, another protocol, no socket, or no entry at all.
func TestAtPIDFailsClosed(t *testing.T) {
	for what, edit := range map[string]func(map[string]any){
		"a reused pid":       func(e map[string]any) { e["procStart"] = "1" },
		"another protocol":   func(e map[string]any) { e["peerProtocol"] = 2 },
		"no socket":          func(e map[string]any) { e["messagingSocketPath"] = "" },
		"another name":       func(e map[string]any) { e["name"] = "worker-b" },
		"another pid inside": func(e map[string]any) { e["pid"] = 1 },
	} {
		reg := claudetest.FakeRegistry(t)
		reg.Add(t, "worker-a", "")
		reg.Edit(t, edit)
		if _, err := claude.AtPID(os.Getpid(), "worker-a"); err == nil {
			t.Errorf("%s: no error", what)
		}
	}
	claudetest.FakeRegistry(t)
	if _, err := claude.AtPID(os.Getpid(), "worker-a"); err == nil {
		t.Error("no entry: no error")
	}
	if _, err := claude.AtPID(0, "worker-a"); !errors.Is(err, claude.ErrNotRunning) {
		t.Errorf("no pid: err = %v, want ErrNotRunning", err)
	}
}

// An entry that exists for the pid but can't be read is an error, never "not running".
func TestAtPIDRefusesAnUnreadableEntry(t *testing.T) {
	reg := claudetest.FakeRegistry(t)
	reg.Add(t, "worker-a", "")
	path := filepath.Join(os.Getenv("HOME"), ".claude", "sessions", fmt.Sprintf("%d.json", os.Getpid()))
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := claude.AtPID(os.Getpid(), "worker-a"); err == nil || errors.Is(err, claude.ErrNotRunning) {
		t.Fatalf("err = %v, want an error that isn't ErrNotRunning", err)
	}
}

func TestTrustedReadsClaudesFolderTrust(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if claude.Trusted("/repo/a") {
		t.Fatal("trusted with no ~/.claude.json")
	}
	cfg := `{"projects":{"/repo/a":{"hasTrustDialogAccepted":true},"/repo/b":{"hasTrustDialogAccepted":false}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if !claude.Trusted("/repo/a") || claude.Trusted("/repo/b") || claude.Trusted("/repo/c") {
		t.Fatal("Trusted disagrees with ~/.claude.json")
	}
}

// A recipient that exited between the core's check and the ring (Claude removed its entry)
// is ErrNotRunning, so bs send says "queued", not "couldn't ring" (motif 11).
func TestAtPIDOfAnExitedProcessIsNotRunning(t *testing.T) {
	claudetest.FakeRegistry(t)
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := claude.AtPID(c.Process.Pid, "worker-a"); !errors.Is(err, claude.ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
	// A live process with no entry is a ring failure, never "not running".
	if _, err := claude.AtPID(os.Getpid(), "worker-a"); err == nil || errors.Is(err, claude.ErrNotRunning) {
		t.Fatalf("live, no entry: err = %v, want an error that isn't ErrNotRunning", err)
	}
}
