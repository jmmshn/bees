// Package claude is the claude_code harness (ADR-0009): it builds Claude Code's launch,
// finds running sessions in Claude's session registry and rings them over their
// cross-session messaging socket.
package claude

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/proc"
)

// ErrNotRunning means the session rung is not running.
var ErrNotRunning = harness.ErrNotRunning

// Session is a live Claude Code session that accepts cross-session messages.
type Session struct {
	Name      string
	PID       int
	ProcStart string // process start time as Claude records it (proc.Start)
	Socket    string
	Token     string // empty when the session has no key file
}

// AtPID reads the registry entry of the Claude process pid, the one a start or queen
// entry recorded (ADR-0009), and checks it is session name. It fails closed (SEC-2): a
// missing or unreadable entry, a reused pid (its procStart differs from the process's
// start time), another name or no messaging socket is an error, so a ring never goes to a
// session the core didn't find. Nothing else in ~/.claude/sessions is read.
func AtPID(pid int, name string) (Session, error) {
	if pid <= 0 {
		return Session{}, fmt.Errorf("%s: no process recorded: %w", name, ErrNotRunning)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Session{}, err
	}
	dir := filepath.Join(home, ".claude", "sessions")
	b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%d.json", pid)))
	if errors.Is(err, os.ErrNotExist) {
		if _, serr := proc.Start(pid); errors.Is(serr, proc.ErrGone) {
			return Session{}, fmt.Errorf("%s exited (pid %d): %w", name, pid, ErrNotRunning) // Claude removed its entry on exit
		}
	}
	if err != nil {
		return Session{}, fmt.Errorf("claude registry for %s (pid %d): %w", name, pid, err)
	}
	var e struct {
		PID                 int
		ProcStart           string
		PeerProtocol        int
		MessagingSocketPath string
		Name                string
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return Session{}, fmt.Errorf("claude registry for %s (pid %d): %w", name, pid, err)
	}
	start, err := proc.Start(pid)
	switch {
	case errors.Is(err, proc.ErrGone):
		return Session{}, fmt.Errorf("%s exited (pid %d): %w", name, pid, ErrNotRunning)
	case err != nil:
		return Session{}, fmt.Errorf("claude session %s (pid %d): %w", name, pid, err)
	case e.PID != pid:
		return Session{}, fmt.Errorf("claude registry file of pid %d names pid %d", pid, e.PID)
	case start != e.ProcStart:
		return Session{}, fmt.Errorf("claude registry entry for pid %d is stale: the pid was reused", pid)
	case e.Name != name:
		return Session{}, fmt.Errorf("the claude session at pid %d is %q, not %s", pid, e.Name, name)
	case e.PeerProtocol != 1 || e.MessagingSocketPath == "":
		return Session{}, fmt.Errorf("claude session %s (pid %d) takes no cross-session messages", name, pid)
	}
	s := Session{Name: e.Name, PID: pid, ProcStart: e.ProcStart, Socket: e.MessagingSocketPath}
	sum := sha256.Sum256([]byte(s.Socket))
	switch b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%d.%s.key", pid, hex.EncodeToString(sum[:])))); {
	case err == nil:
		var k struct{ PeerToken string }
		if err := json.Unmarshal(b, &k); err != nil {
			return Session{}, fmt.Errorf("claude session %s key file: %w", name, err)
		}
		s.Token = k.PeerToken
	case !errors.Is(err, os.ErrNotExist): // no key file: the session takes rings without one
		return Session{}, fmt.Errorf("claude session %s key file: %w", name, err)
	}
	return s, nil
}

// Trusted reports whether Claude has accepted its folder-trust dialog for dir, the main
// checkout of a repo (its worktrees share the answer). It only reads ~/.claude.json.
// ponytail: an undocumented file of Claude's; anything unreadable counts as untrusted,
// which only costs a needless warning.
func Trusted(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return false
	}
	var cfg struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool
		}
	}
	return json.Unmarshal(b, &cfg) == nil && cfg.Projects[filepath.Clean(dir)].HasTrustDialogAccepted
}

// Ring sends text into s as a user prompt: an NDJSON auth line when s has a token, then
// the user line, then a half-close. Claude sends no reply.
func Ring(s Session, text string, p harness.Priority) error {
	c, err := net.DialTimeout("unix", s.Socket, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	enc := json.NewEncoder(c) // writes one JSON value per line
	if s.Token != "" {
		if err := enc.Encode(map[string]string{"type": "auth", "token": s.Token}); err != nil {
			return err
		}
	}
	err = enc.Encode(map[string]any{
		"type":     "user",
		"message":  map[string]string{"role": "user", "content": text},
		"priority": p,
		"msg_id":   uuid(),
	})
	if err != nil {
		return err
	}
	return c.(*net.UnixConn).CloseWrite()
}

// uuid returns a random (version 4) UUID.
func uuid() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
