package claude_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmmshn/bees/internal/harness/claude"
	"github.com/jmmshn/bees/internal/harness/claude/claudetest"
)

func TestRingSendsAuthThenUserLineThenEOF(t *testing.T) {
	reg := claudetest.FakeRegistry(t)
	got := reg.Add(t, "worker-a", "tok123")
	s, err := claude.Find("worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := claude.Ring(s, "hello", claude.Now); err != nil {
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
	s, err := claude.Find("worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := claude.Ring(s, "hello", claude.Next); err != nil {
		t.Fatal(err)
	}
	lines := got()
	if len(lines) != 1 || lines[0]["type"] != "user" || lines[0]["priority"] != "next" {
		t.Fatalf("lines = %v, want one user line with priority next", lines)
	}
}

func TestFindRejectsStaleStartTime(t *testing.T) {
	reg := claudetest.FakeRegistry(t)
	reg.Add(t, "worker-a", "")
	reg.Edit(t, func(e map[string]any) { e["procStart"] = "1" }) // the pid was reused by another process
	if _, err := claude.Find("worker-a"); !errors.Is(err, claude.ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}

func TestFindRejectsOtherPeerProtocols(t *testing.T) {
	reg := claudetest.FakeRegistry(t)
	reg.Add(t, "worker-a", "")
	reg.Edit(t, func(e map[string]any) { e["peerProtocol"] = 2 })
	if _, err := claude.Find("worker-a"); !errors.Is(err, claude.ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}

func TestFindUnknownNameIsNotRunning(t *testing.T) {
	claudetest.FakeRegistry(t)
	if _, err := claude.Find("nobody"); !errors.Is(err, claude.ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}

func TestFindRejectsDeadPids(t *testing.T) {
	reg := claudetest.FakeRegistry(t)
	reg.Add(t, "worker-a", "")
	reg.Edit(t, func(e map[string]any) { e["pid"] = 1 << 30 }) // above Linux's pid_max
	if _, err := claude.Find("worker-a"); !errors.Is(err, claude.ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}

func TestFindRejectsSessionsWithoutASocket(t *testing.T) {
	reg := claudetest.FakeRegistry(t)
	reg.Add(t, "worker-a", "")
	reg.Edit(t, func(e map[string]any) { e["messagingSocketPath"] = "" })
	if _, err := claude.Find("worker-a"); !errors.Is(err, claude.ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
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
