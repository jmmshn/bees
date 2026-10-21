// Package harness is the one port between bs's core and the agent program in a pane
// (ADR-0009): how it starts and how it is rung. The core decides everything about a
// launch; an adapter only renders it.
package harness

import (
	"errors"
	"regexp"

	"github.com/jmmshn/bees/internal/guards"
	"github.com/jmmshn/bees/internal/prompt"
)

// Default is the harness of an entry recorded before entries named one, and of a config
// that sets none.
const Default = "claude_code"

// ErrNotRunning means no live session has the name rung.
var ErrNotRunning = errors.New("not running")

// ErrNoResume means the harness can't resume a conversation, so a start that would
// continue one is refused rather than started fresh silently.
var ErrNoResume = errors.New("its harness can't resume a conversation yet")

// ErrNoRing means the harness has no way to ring a running session yet: the message waits
// for the session's next bs inbox.
var ErrNoRing = errors.New("its harness has no ring yet")

// ErrBadPath means a Start.Path dir can't be used: the core's check (absolute, a
// directory) or the adapter's own (a character its rendering can't hold) failed.
var ErrBadPath = errors.New("not a --path dir")

// Priority is when the receiving session acts on a ring.
type Priority string

const (
	Now  Priority = "now"  // interrupt the current turn
	Next Priority = "next" // after the current turn
)

// Start is everything the core decides about one launch. Adapters render it; they never
// choose a name, a directory or a prompt.
type Start struct {
	Role       prompt.Role
	Name       string       // session name (ADR-0006)
	Dir        string       // the workspace directory
	PromptFile string       // the role prompt file
	Doorbell   string       // first user message; "" for the queen
	Continue   bool         // resume the last conversation in Dir
	Guards     guards.Rules // built by the core; zero for the queen
	Path       []string     // absolute dirs put first on the PATH of the session's shell commands (bs spawn --path)
}

// Launch is what Prepare returns. Prepare is pure: the core writes each of Files (0600,
// in the cache dir, named <name>-<stem>-*<ext> from its key), fills the {{key}} it stands
// for in Args with that file's path, and removes <name>-[a-z]*-* on bs end and before each
// start. A key is lowercase and starts with a letter, so a worker's files never match a
// glob of another worker whose name it extends (bees-12 and bees-12-3).
type Launch struct {
	Args   []string          // after `herdr agent start … --`, or after Kind() for bs queen's exec
	Files  map[string]string // key → content, e.g. "settings.json" → Claude's --settings
	Record map[string]string // facts known before the start that a later start needs
}

// FileKey is the shape of a Launch.Files key.
var FileKey = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z]+)?$`)

// Target is a session as the core found it.
type Target struct {
	Name, Pane string
	PID        int // the agent's pid; 0 until the core finds sessions from herdr (bees-12.10)
}

// Harness is one agent program bs can start and ring.
type Harness interface {
	Kind() string                                 // herdr --kind (claude, agy), also the binary bs queen execs
	Prepare(st Start) (Launch, error)             // pure: render guards, files and argv
	Ring(t Target, text string, p Priority) error // the doorbell; ErrNotRunning when t isn't running, ErrNoRing when the harness can't ring
}
