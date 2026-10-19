// Package proc reads the two facts bs needs about another process: its parent, and its
// start time in the form Claude Code records it in ~/.claude/sessions (procStart).
package proc

import "errors"

// ErrGone means the process doesn't exist or is a zombie: Claude counts neither as running.
var ErrGone = errors.New("no such running process")
