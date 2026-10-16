// Package claude finds running Claude Code sessions in Claude's session registry and
// rings them over their cross-session messaging socket (SPEC "Claude adapter").
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
	"strings"
	"time"
)

// ErrNotRunning means no live session has that name.
var ErrNotRunning = errors.New("not running")

// Priority is when the receiving session acts on a ring.
type Priority string

const (
	Now  Priority = "now"  // interrupt the current turn
	Next Priority = "next" // after the current turn
)

// Session is a live Claude Code session that accepts cross-session messages.
type Session struct {
	Name      string
	PID       int
	ProcStart string // /proc start time in clock ticks, as Claude records it
	Socket    string
	Token     string // empty when the session has no key file
	startedAt int64
}

// List returns every live, compatible session in ~/.claude/sessions: peerProtocol 1, a
// socket, and a pid whose start time still matches (so a reused pid isn't mistaken for it).
func List() ([]Session, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".claude", "sessions")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue // the session exited between glob and read
		}
		var e struct {
			PID                 int
			ProcStart           string
			PeerProtocol        int
			MessagingSocketPath string
			Name                string
			StartedAt           int64
		}
		if json.Unmarshal(b, &e) != nil || e.PeerProtocol != 1 || e.MessagingSocketPath == "" {
			continue
		}
		if start, err := procStart(e.PID); err != nil || start != e.ProcStart {
			continue
		}
		s := Session{Name: e.Name, PID: e.PID, ProcStart: e.ProcStart, Socket: e.MessagingSocketPath, startedAt: e.StartedAt}
		sum := sha256.Sum256([]byte(s.Socket))
		if b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%d.%s.key", e.PID, hex.EncodeToString(sum[:])))); err == nil {
			var k struct{ PeerToken string }
			if json.Unmarshal(b, &k) == nil {
				s.Token = k.PeerToken
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// Find returns the live session called name, the newest if several share it.
func Find(name string) (Session, error) {
	ss, err := List()
	if err != nil {
		return Session{}, err
	}
	var found Session
	for _, s := range ss {
		if s.Name == name && (found.Name == "" || s.startedAt > found.startedAt) {
			found = s
		}
	}
	if found.Name == "" {
		return Session{}, fmt.Errorf("%s: %w", name, ErrNotRunning)
	}
	return found, nil
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
func Ring(s Session, text string, p Priority) error {
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

// procStart is field 22 of /proc/<pid>/stat, the process start time in clock ticks.
// ponytail: Linux /proc only, like the rest of bs.
func procStart(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	// The command name is in parentheses and may contain spaces; fields resume after ')'.
	i := strings.LastIndexByte(string(b), ')')
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return "", fmt.Errorf("pid %d: short /proc stat", pid)
	}
	return f[19], nil
}
