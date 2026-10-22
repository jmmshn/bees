// Package core is bs's core over its ports (ADR-0010): spawn, the handoff, end and prune,
// identity and the background-work gate. It reads every fact from the logbook and the
// world, never from what a caller typed (Rule D), and every refusal is an *Exit that says
// what to run next (Rule G).
package core

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/herdr"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// Core is one project's bs.
type Core struct {
	Logbook     *logbook.Logbook
	Project     string
	Run         port.Runner // herdr, git, gh
	Harness     port.Harness
	HarnessName string // the config name entries record: claude_code
	Proc        port.Proc
	Beads       port.Beads
	Repo        string   // paths.repo: the project's checkout
	Worktrees   string   // where git_worktree workspaces go: <Worktrees>/<name>
	Kind        string   // the workspace kind, worked out or set (config.Kind)
	KindWhy     string   // how config came to it
	ConfigDir   string   // the config dir the spawn ran under, recorded
	CacheDir    string   // per-start files
	WorkerPath  []string // worker.path: dirs first on a worker's PATH
	Timing      Timing
	Warn        func(msg string) // prints a fallback bs took (Rule G); nil drops it
}

// warn says a fallback bs took.
func (c *Core) warn(format string, a ...any) {
	if c.Warn != nil {
		c.Warn(fmt.Sprintf(format, a...))
	}
}

// Timing is how long the core waits. Tests shorten it.
type Timing struct {
	Confirm    time.Duration // start confirmation: the harness process in its pane
	Poll       time.Duration
	SecondLook time.Duration // the gate's second look, dropping short-lived children
	Gate       time.Duration // the whole gate check
	AfterErr   time.Duration // after herdr's start erred: how long to look for the harness anyway (bees-12.49)
	Busy       time.Duration // how long agent_pane_busy is retried (bees-12.33)
	Stop       time.Duration // SIGTERM before SIGKILL
}

// DefaultTiming is ADR-0010's: confirmation polled every 250 ms within 20 s, 5 s after a
// herdr error, agent_pane_busy retried for 8 s.
var DefaultTiming = Timing{Confirm: 20 * time.Second, Poll: 250 * time.Millisecond, SecondLook: time.Second, Stop: 3 * time.Second,
	Gate: 5 * time.Second, AfterErr: 5 * time.Second, Busy: 8 * time.Second}

func (c *Core) timing() Timing {
	t, d := c.Timing, DefaultTiming
	if t.Confirm == 0 {
		return d
	}
	t.Poll, t.SecondLook, t.Stop = cmp.Or(t.Poll, d.Poll), cmp.Or(t.SecondLook, d.SecondLook), cmp.Or(t.Stop, d.Stop)
	t.Gate, t.AfterErr, t.Busy = cmp.Or(t.Gate, d.Gate), cmp.Or(t.AfterErr, d.AfterErr), cmp.Or(t.Busy, d.Busy)
	return t
}

// herdr is the client of session's herdr server.
func (c *Core) herdr(session string) herdr.Client { return herdr.Client{Run: c.Run, Session: session} }

// git runs git in dir and returns its trimmed stdout.
func (c *Core) git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := c.Run.Run(ctx, dir, append([]string{"git", "-C", dir}, args...)...)
	return strings.TrimSpace(string(out)), err
}

// Exit codes. Main's keep their meaning; 7, 8 and 9 are ADR-0010's.
const (
	ExitFail       = 1 // something failed; the message says what
	ExitRefused    = 2 // refused before anything changed: usage, a bad goal, a missing record
	ExitKept       = 3 // prune kept a workspace; end: herdr didn't answer
	ExitUnknown    = 4 // prune couldn't check
	ExitBackground = 5 // the gate found background work
	ExitCantCheck  = 6 // a check couldn't run: never read as "no"
	ExitHandoff    = 7 // a handoff check failed
	ExitStart      = 8 // a start bs couldn't confirm
	ExitIncomplete = 9 // bs end on an incomplete goal
)

// Exit is a refusal or failure with its exit code and the next command to run.
type Exit struct {
	Code int
	Msg  string
	Next string // the command to run next; "" when there is none
	Err  error  // what caused it, if anything
}

func (e *Exit) Error() string {
	s := e.Msg
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	if e.Next != "" {
		s += "\n  next: " + e.Next
	}
	return s
}

func (e *Exit) Unwrap() error { return e.Err }

func exit(code int, next string, err error, format string, a ...any) *Exit {
	return &Exit{Code: code, Msg: fmt.Sprintf(format, a...), Next: next, Err: err}
}

// Name is a bead's worker session name (ADR-0006): its id with . → -.
func Name(bead string) string { return strings.ReplaceAll(bead, ".", "-") }

// sleep waits d, or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
