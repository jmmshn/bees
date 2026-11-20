// Package proc reads what bs needs about another process: its parent, its start time in
// the form Claude Code records it in ~/.claude/sessions (procStart), its name, and its
// descendants. A recorded process is the one fact behind a session's liveness (ADR-0009).
package proc

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// Process is a process as bs records it: a pid is only that process while its start time
// matches, so a reused pid names no one.
type Process struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

// Background is a process running below a worker's harness that its start didn't see:
// background work (ADR-0009 amendment 1).
type Background struct {
	Process
	Command string `json:"command"`
	Ports   []int  `json:"ports,omitempty"` // TCP ports it listens on
}

// String is the process as bs lists it on one line: pid, command and any listening port.
// The command is cut to commandWidth: a harness's shell wrapper runs to many lines
// (Claude's sources a snapshot before the tool call's own command).
func (b Background) String() string {
	c := []rune(strings.Join(strings.Fields(b.Command), " "))
	if len(c) > commandWidth {
		c = append(c[:commandWidth-1], '…')
	}
	s := fmt.Sprintf("pid %-6d %s", b.PID, string(c))
	for _, p := range b.Ports {
		s += fmt.Sprintf("   listening :%d", p)
	}
	return s
}

// commandWidth bounds the command Background.String shows.
const commandWidth = 100

// descendants returns the processes below root in a table of every process and its parent,
// level by level: children, then grandchildren.
func descendants(root int, all []Process, parent map[int]int) []Process {
	var out []Process
	for level := []int{root}; len(level) > 0; {
		var next []int
		for _, p := range all {
			if slices.Contains(level, parent[p.PID]) && p.PID != root {
				out = append(out, p)
				next = append(next, p.PID)
			}
		}
		level = next
	}
	return slices.Clip(out)
}

// resolve is dir with its symlinks resolved, as the process table gives a cwd: a
// symlinked TMPDIR (macOS /tmp) must still match. Done once per lookup, not per process.
func resolve(dir string) string {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return r
	}
	return dir
}

// inside reports whether cwd, an absolute path as the process table gives it, is dir (already
// resolved) or below it.
func inside(cwd, dir string) bool {
	rel, err := filepath.Rel(dir, cwd)
	return err == nil && filepath.IsAbs(cwd) && rel != ".." && !strings.HasPrefix(rel, "../")
}

// lsofCwds is the pids lsof's field output (-F pn: p<pid>, then n<path>) names with a cwd
// inside dir (resolved), in order.
func lsofCwds(out, dir string) []int {
	var pids []int
	pid := 0
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "n") && pid > 0 && inside(line[1:], dir) && !slices.Contains(pids, pid):
			pids = append(pids, pid)
		}
	}
	return pids
}

// ErrNoEnviron is a process table bs can't read another process's environment from
// (anything but Linux): nothing is tied to a worker by its environment there.
var ErrNoEnviron = errors.New("can't read another process's environment on this system")

// ErrGone means// ErrGone means the process doesn't exist or is a zombie: Claude counts neither as running.
var ErrGone = errors.New("no such running process")

// Alive reports whether pid is running with start time start (as Start reports it), so a
// reused pid is not taken for the process recorded (ADR-0009 SEC-2). A failed lookup is
// an error, never "gone" (motif 7).
func Alive(pid int, start string) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	s, err := Start(pid)
	if errors.Is(err, ErrGone) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s == start, nil
}

// Signal sends sig to p only while p is still the process recorded: alive with its start
// time, so a reused pid is never signalled (motif 12).
func Signal(p Process, sig syscall.Signal) error {
	alive, err := Alive(p.PID, p.Start)
	if err != nil {
		return err
	}
	if !alive {
		return fmt.Errorf("pid %d: %w", p.PID, ErrGone)
	}
	return syscall.Kill(p.PID, sig)
}
