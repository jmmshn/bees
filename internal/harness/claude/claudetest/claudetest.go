// Package claudetest fakes Claude Code's session registry for tests.
package claudetest

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Registry is a fake ~/.claude/sessions under a temp HOME. Its one session is this test
// process, so the pid is alive and its start time real.
type Registry struct{ dir string }

// FakeRegistry points HOME at an empty temp dir with a session registry.
func FakeRegistry(t *testing.T) Registry {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return Registry{dir}
}

// Add registers this process as session name, listening on a fresh unix socket, with a
// key file when token is set. The returned func waits for the NDJSON lines of the first
// connection, failing the test if none arrives.
func (r Registry) Add(t *testing.T, name, token string) func() []map[string]any {
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
	start, err := procStart(pid)
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
func (r Registry) Edit(t *testing.T, f func(map[string]any)) {
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

func (r Registry) write(t *testing.T, name string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(filepath.Join(r.dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// procStart is field 22 of /proc/<pid>/stat, the process start time in clock ticks.
func procStart(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
	if len(f) < 20 {
		return "", fmt.Errorf("pid %d: short /proc stat", pid)
	}
	return f[19], nil
}
