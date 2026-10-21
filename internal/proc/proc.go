// Package proc reads the two facts bs needs about another process: its parent, and its
// start time in the form Claude Code records it in ~/.claude/sessions (procStart).
package proc

import "errors"

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
