package claude_test

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/harness/claude"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

func TestRingSendsAuthThenUserLineThenEOF(t *testing.T) {
	reg := fakeRegistry(t)
	got := reg.Add(t, "worker-a", "tok123")
	s, err := claude.AtPID(reg.home, os.Getpid(), "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := claude.Ring(s, "hello", port.Now); err != nil {
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
	reg := fakeRegistry(t)
	got := reg.Add(t, "worker-a", "")
	s, err := claude.AtPID(reg.home, os.Getpid(), "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := claude.Ring(s, "hello", port.Next); err != nil {
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
		reg := fakeRegistry(t)
		reg.Add(t, "worker-a", "")
		reg.Edit(t, edit)
		if _, err := claude.AtPID(reg.home, os.Getpid(), "worker-a"); err == nil {
			t.Errorf("%s: no error", what)
		}
	}
	reg := fakeRegistry(t)
	if _, err := claude.AtPID(reg.home, os.Getpid(), "worker-a"); err == nil {
		t.Error("no entry: no error")
	}
	if _, err := claude.AtPID(reg.home, 0, "worker-a"); !errors.Is(err, claude.ErrNotRunning) {
		t.Errorf("no pid: err = %v, want ErrNotRunning", err)
	}
}

// An entry that exists for the pid but can't be read is an error, never "not running".
func TestAtPIDRefusesAnUnreadableEntry(t *testing.T) {
	reg := fakeRegistry(t)
	reg.Add(t, "worker-a", "")
	path := filepath.Join(reg.home, ".claude", "sessions", fmt.Sprintf("%d.json", os.Getpid()))
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := claude.AtPID(reg.home, os.Getpid(), "worker-a"); err == nil || errors.Is(err, claude.ErrNotRunning) {
		t.Fatalf("err = %v, want an error that isn't ErrNotRunning", err)
	}
}

// A recipient that exited between the core's check and the ring (Claude removed its entry)
// is ErrNotRunning, so bs send says "queued", not "couldn't ring" (motif 11).
func TestAtPIDOfAnExitedProcessIsNotRunning(t *testing.T) {
	reg := fakeRegistry(t)
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := claude.AtPID(reg.home, c.Process.Pid, "worker-a"); !errors.Is(err, claude.ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
	// A live process with no entry is a ring failure, never "not running".
	if _, err := claude.AtPID(reg.home, os.Getpid(), "worker-a"); err == nil || errors.Is(err, claude.ErrNotRunning) {
		t.Fatalf("live, no entry: err = %v, want an error that isn't ErrNotRunning", err)
	}
}

// registry is a fake ~/.claude/sessions under a temp home. Its one session is this test
// process, so the pid is alive and its start time real.
type registry struct{ home, dir string }

// fakeRegistry is an empty session registry in a temp home.
func fakeRegistry(t *testing.T) registry {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return registry{home, dir}
}

// Add registers this process as session name, listening on a fresh unix socket, with a
// key file when token is set. The returned func waits for the NDJSON lines of the first
// connection, failing the test if none arrives.
func (r registry) Add(t *testing.T, name, token string) func() []map[string]any {
	t.Helper()
	sockDir, err := os.MkdirTemp("", "cc") // unix socket paths must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan []map[string]any, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var lines []map[string]any
		sc := bufio.NewScanner(c)
		for sc.Scan() { // ends at EOF, i.e. the sender's half-close
			var m map[string]any
			_ = json.Unmarshal(sc.Bytes(), &m)
			lines = append(lines, m)
		}
		got <- lines
	}()
	rung := func() []map[string]any {
		t.Helper()
		select {
		case lines := <-got:
			return lines
		case <-time.After(5 * time.Second):
			t.Fatalf("%s was never rung", name)
			return nil
		}
	}

	pid := os.Getpid()
	start, err := proc.Start(pid)
	if err != nil {
		t.Fatal(err)
	}
	r.write(t, fmt.Sprintf("%d.json", pid), map[string]any{
		"pid": pid, "procStart": start, "peerProtocol": 1,
		"messagingSocketPath": sock, "name": name, "startedAt": 1,
	})
	if token != "" {
		sum := sha256.Sum256([]byte(sock))
		r.write(t, fmt.Sprintf("%d.%s.key", pid, hex.EncodeToString(sum[:])), map[string]any{"peerToken": token})
	}
	return rung
}

// Edit rewrites this process's registry entry.
func (r registry) Edit(t *testing.T, f func(map[string]any)) {
	t.Helper()
	name := fmt.Sprintf("%d.json", os.Getpid())
	b, err := os.ReadFile(filepath.Join(r.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	var e map[string]any
	_ = json.Unmarshal(b, &e)
	f(e)
	r.write(t, name, e)
}

func (r registry) write(t *testing.T, name string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(filepath.Join(r.dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}
