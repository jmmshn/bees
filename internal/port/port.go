// Package port is every boundary between bs's core and the world (ADR-0010): the harness
// in a pane, the commands bs runs (herdr, git, gh, a keeper's argv), the process table and
// the bead tracker. The core sees only these; tests put fakes behind them that encode what
// the real tool refuses (motif 13).
package port

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"

	"github.com/jmmshn/bees/internal/proc"
)

// Runner runs one external command, argv only, never through a shell: argv[0] is the
// program. A command that ran and exited non-zero is an *ExitError carrying its output;
// any other error means it couldn't run.
type Runner interface {
	Run(ctx context.Context, dir string, argv ...string) (stdout []byte, err error)
}

// ExitError is a command that ran and exited non-zero.
type ExitError struct {
	Argv   []string
	Code   int
	Stdout []byte
	Stderr []byte
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(string(e.Stderr))
	if msg == "" {
		msg = strings.TrimSpace(string(e.Stdout))
	}
	return fmt.Sprintf("%s: exit %d: %s", strings.Join(e.Argv, " "), e.Code, msg)
}

// Harness is one agent program bs can start and ring (ADR-0010, the pilot's three
// methods kept).
type Harness interface {
	Kind() string                                 // herdr --kind and the prompts' harness: claude | omp | agy
	Program() string                              // the program a launch runs, a name on PATH or an absolute path: Kind's own, or another (agy.bin); a spawn records it
	Prepare(st Start) (Launch, error)             // pure: argv, per-start files, guards rendered, Record
	Owns(argv []string, name string) bool         // argv, of a process running the spawn's program, is session name's launch
	Ring(t Target, text string, p Priority) error // ErrNotRunning | ErrNoRing | a write error
	Observe(env []string) map[string]string       // pure: post-start facts a later start needs, read from the env of a bs the session runs
}

// ErrNotRunning means the session rung is not running: its recorded process is gone.
var ErrNotRunning = errors.New("not running")

// ErrNoRing means the harness has no way to ring a running session: the message waits for
// the session's next bs inbox.
var ErrNoRing = errors.New("its harness has no ring")

// Truster is a harness that only starts in a folder it was told to trust, exact path:
// spawn trusts the workspace folder it just made, and prune untrusts it once removed.
// Both are no-ops when already so. Claude's is for a folder outside any git checkout (a
// command workspace's): a repo's trust, which its worktrees share, is the keeper's.
type Truster interface {
	Trust(dir string) error
	Untrust(dir string) error
}

// Priority is when the receiving session acts on a ring.
type Priority string

const (
	Now  Priority = "now"  // interrupt the current turn
	Next Priority = "next" // after the current turn
)

// Role is which prompt a session starts with.
type Role string

const (
	Queen  Role = "queen"
	Worker Role = "worker"
)

// Start is everything the core decides about one launch. Adapters render it; they never
// choose a name, a directory or a prompt.
type Start struct {
	Role       Role
	Name       string            // session name (ADR-0006)
	Dir        string            // the workspace directory
	Branch     string            // the worker's own branch; "" for a workspace with none
	PushTo     string            // a push goal's target head branch, which Branch is pushed onto; "" for none
	Repo       string            // the project dir the workspace was made from
	PromptFile string            // the assembled role prompt's file
	Prompt     string            // its text, for a harness that takes the prompt inline (agy's agent body)
	Doorbell   string            // first user message; "" for the queen
	Continue   bool              // resume the recorded conversation
	Record     map[string]string // the spawn's recorded Launch.Record, on a resume
	Deny       []string          // the guard rules, harness-neutral, built by the core; none for the queen
	Path       []string          // absolute dirs first on the PATH of the session's shell commands
	// ShellTimeout is how long one shell command of the session runs before its harness
	// cuts it, by default too: bs done --wait's whole wait fits in one (0: the harness's own).
	ShellTimeout time.Duration
}

// Launch is what Prepare returns. The core writes each of Files (0600, in the cache dir,
// named <name>-<key>-*), fills the {{key}} it stands for in Args with that file's path,
// and removes <name>-[a-z]*-* on bs end, at prune and before each start. Dir is a tree the
// same way: one per-start directory, <name>-dir-* (0700), holding each file at its
// relative path (0600), its path filling {{dir}}.
type Launch struct {
	Args   []string          // after `herdr agent start … --`
	Files  map[string]string // key → content
	Dir    map[string]string // relative path → content
	Record map[string]string // facts known before the start that a later start needs (a conversation id bs chose)
	Unset  []string          // env vars the harness must not inherit: the core unsets them in the pane's shell first, and bs queen drops them from its exec
}

// Target is a session as the core found it: alive with its recorded start time.
type Target struct {
	Name, Session, Pane string
	PID                 int
}

// Proc is the process table: a recorded process is the one fact behind liveness, and
// its descendants are what the background-work gate walks (ADR-0010).
type Proc interface {
	Alive(p proc.Process) (bool, error) // running with its recorded start time; a failed lookup is an error, never "gone"
	Start(pid int) (string, error)      // proc.ErrGone for a process that doesn't run
	Parent(pid int) (int, error)
	Group(pid int) (int, error)
	Descendants(pid int) ([]proc.Process, error) // nearest first
	Command(pid int) (string, error)
	Exe(pid int) (string, error)
	Signal(p proc.Process, sig syscall.Signal) error                  // only while p is still the process recorded
	Listening(ctx context.Context, pids []int) (map[int][]int, error) // TCP ports each listens on
}

// Bead is one bead as bs reads it.
type Bead struct {
	ID, Title, Description string
	Notes, Design          string
	Parent                 string
	Status                 string // the tracker's: open, in_progress, closed, …
}

// ErrNoBead is the tracker saying a bead doesn't exist; any other failure is the tracker
// not answering, never "no such bead" (Rule G).
var ErrNoBead = errors.New("no such bead")

// Beads is the bead tracker.
type Beads interface {
	Show(ctx context.Context, id string) (Bead, error)
	Children(ctx context.Context, parent string) ([]string, error) // ids of beads directly under parent
}
