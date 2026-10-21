// Package proc reads what bs needs about another process: its parent, its start time in
// the form Claude Code records it in ~/.claude/sessions (procStart), its name, and its
// descendants. A recorded process is the one fact behind a session's liveness (ADR-0009).
package proc

import (
	"errors"
	"fmt"
	"slices"
	"strings"
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

// ErrGone means the process doesn't exist or is a zombie: Claude counts neither as running.
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
