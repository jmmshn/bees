// Package checks runs a keeper's argv from config: a cmd: check of a custom goal, or a
// workspace command (ADR-0010). It is argv only, never a shell. Placeholders come only
// from the record and from checked handoff values, each filling one whole argument; a
// value starting with - is refused and a ref must pass git check-ref-format; the cwd is
// the recorded dir, the env only PATH, HOME and LANG; a timeout kills the process group.
package checks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jmmshn/bees/internal/port"
)

// Cmd is an argv with its bound, as config gives it and a spawn freezes it.
type Cmd struct {
	Argv    []string      `json:"argv"`
	Timeout time.Duration `json:"timeout"`
}

// Value is a placeholder's value. Ref marks a git ref, which must pass
// git check-ref-format.
type Value struct {
	S   string
	Ref bool
}

// Result is a command that ran to its exit.
type Result struct {
	Argv   []string // as it ran, placeholders filled
	Code   int
	Output []byte // stdout and stderr, interleaved
	Stdout []byte // stdout alone: a command whose answer is JSON
}

var (
	// ErrBadArgv is an argv bs refuses to run: a placeholder that isn't a whole argument,
	// an unknown placeholder, or a value that could be read as a flag or a bad ref.
	ErrBadArgv = errors.New("refused argv")
	// ErrCantRun is a command that couldn't start or didn't finish: never a fail (Rule G).
	ErrCantRun = errors.New("couldn't run")
	// ErrTimeout is a command killed at its timeout, with its process group.
	ErrTimeout = errors.New("timed out")
)

// DefaultTimeout bounds a Cmd that names none.
const DefaultTimeout = 60 * time.Second

// Keys are the placeholders bs fills from a spawn's record: the workspace dir, the
// worker's name, its bead, the project and its repo (paths.repo), and a git_worktree's
// branch, base and start (its tip at spawn). A workspace kind with no branch fills none of
// the last three. A worker names nothing (ADR-0010 Amendment 4), so no placeholder comes
// from it.
var Keys = append(slices.Clone(WorkspaceKeys), "branch", "base", "start")

// WorkspaceKeys are the placeholders every workspace kind fills, and all a
// [workspace.command] argv may name: a command workspace has no branch.
var WorkspaceKeys = []string{"dir", "name", "bead", "project", "repo"}

// Check refuses, before anything runs, an argv Fill could never fill: an empty one, a
// placeholder argv[0], a brace that isn't a whole-argument placeholder, or one not
// among keys.
func Check(argv, keys []string) error {
	if len(argv) == 0 || argv[0] == "" {
		return errors.New("an argv is required")
	}
	if strings.ContainsAny(argv[0], "{}") {
		return fmt.Errorf("argv[0] %q can't be a placeholder", argv[0])
	}
	var errs []error
	for i, a := range argv[1:] {
		m := placeholder.FindStringSubmatch(a)
		switch {
		case m == nil && strings.ContainsAny(a, "{}"):
			errs = append(errs, fmt.Errorf("argument %d %q: a placeholder fills one whole argument", i+1, a))
		case m == nil, slices.Contains(keys, m[1]):
		case m[1] == "pr", m[1] == "change":
			errs = append(errs, fmt.Errorf("argument %d %s: a worker names no change, so bs has none to fill; find it from the workspace ({dir} or {name})", i+1, a))
		default:
			errs = append(errs, fmt.Errorf("argument %d %s: no such placeholder (there are {%s})", i+1, a, strings.Join(keys, "}, {")))
		}
	}
	return errors.Join(errs...)
}

// placeholder is a whole-argument placeholder; any other brace in an argument is refused.
var placeholder = regexp.MustCompile(`^\{([a-z][a-z0-9_]*)\}$`)

// Fill returns argv with each whole-argument {key} replaced by vals[key]. argv[0] is
// never a placeholder. A ref git couldn't check (within ctx) is ErrCantRun, not ErrBadArgv.
func Fill(ctx context.Context, argv []string, vals map[string]Value) ([]string, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, fmt.Errorf("%w: empty argv", ErrBadArgv)
	}
	if strings.ContainsAny(argv[0], "{}") {
		return nil, fmt.Errorf("%w: argv[0] %q can't be a placeholder", ErrBadArgv, argv[0])
	}
	out := make([]string, len(argv))
	for i, a := range argv {
		m := placeholder.FindStringSubmatch(a)
		if m == nil {
			if strings.ContainsAny(a, "{}") {
				return nil, fmt.Errorf("%w: argument %d %q: a placeholder fills one whole argument", ErrBadArgv, i, a)
			}
			out[i] = a
			continue
		}
		v, ok := vals[m[1]]
		switch {
		case !ok:
			return nil, fmt.Errorf("%w: argument %d: no placeholder %s", ErrBadArgv, i, a)
		case v.S == "":
			return nil, fmt.Errorf("%w: argument %d: %s is empty", ErrBadArgv, i, a)
		case strings.HasPrefix(v.S, "-"):
			return nil, fmt.Errorf("%w: argument %d: %s = %q starts with -", ErrBadArgv, i, a, v.S)
		case v.Ref:
			if ok, err := refOK(ctx, v.S); err != nil {
				return nil, fmt.Errorf("%w: argument %d: checking %s = %q: %w", ErrCantRun, i, a, v.S, err)
			} else if !ok {
				return nil, fmt.Errorf("%w: argument %d: %s = %q isn't a valid git ref", ErrBadArgv, i, a, v.S)
			}
		}
		out[i] = v.S
	}
	return out, nil
}

// refOK reports whether git check-ref-format accepts s as a branch name. A git that
// couldn't run or answer is an error, never "bad ref" (Rule G).
func refOK(ctx context.Context, s string) (bool, error) {
	_, err := port.Exec{}.Run(ctx, "", "git", "check-ref-format", "--branch", s)
	var ee *port.ExitError
	if errors.As(err, &ee) {
		return false, nil
	}
	return err == nil, err
}

// env is the only environment a command gets: PATH, HOME and LANG, as bs has them
// (getenv: bs's own environment, which an in-process caller passes in).
func env(getenv func(string) string) []string {
	var out []string
	for _, k := range []string{"PATH", "HOME", "LANG"} {
		if v := getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// LookPath finds program on path (a PATH value), as a command bs runs would: a name
// with a / is itself, when it is executable. ponytail: exec.LookPath searches only bs's own
// PATH, and an in-process caller's (a test's toy, a pane's) is another; it skips empty
// PATH entries, which exec would read as the cwd, and never reads PATHEXT.
func LookPath(path, program string) (string, bool) {
	if strings.Contains(program, "/") {
		fi, err := os.Stat(program)
		return program, err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
	}
	for _, d := range filepath.SplitList(path) {
		if d == "" {
			continue
		}
		at := filepath.Join(d, program)
		if fi, err := os.Stat(at); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return at, true
		}
	}
	return "", false
}

// Run fills c's argv from vals and runs it in dir, with getenv's PATH, HOME and LANG (nil:
// bs's process environment). A non-zero exit is a Result, not an error; a refused argv is
// ErrBadArgv; a command that couldn't start (argv[0] on no PATH among them), or ran past
// its timeout (ErrTimeout, its whole process group killed), is ErrCantRun.
func Run(ctx context.Context, c Cmd, dir string, vals map[string]Value, getenv func(string) string) (Result, error) {
	argv, err := Fill(ctx, c.Argv, vals)
	if err != nil {
		return Result{}, err
	}
	return Exec(ctx, argv, c.Timeout, dir, getenv)
}

// Exec runs argv, filled already, as Run does.
func Exec(ctx context.Context, argv []string, timeout time.Duration, dir string, getenv func(string) string) (Result, error) {
	if dir == "" {
		return Result{}, fmt.Errorf("%w: no recorded dir to run %s in", ErrCantRun, argv[0])
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	// exec would look a bare name up on bs's own PATH, not the one the command gets.
	prog := argv[0]
	if !strings.Contains(prog, "/") {
		at, ok := LookPath(getenv("PATH"), prog)
		if !ok {
			return Result{}, fmt.Errorf("%w: %s is not on PATH", ErrCantRun, prog)
		}
		prog = at
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, argv[1:]...)
	cmd.Args[0] = argv[0]
	var out, stdout bytes.Buffer
	both := &locked{w: &out} // stdout's and stderr's copies write it at once
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env(getenv), io.MultiWriter(both, &stdout), both
	err := port.RunGroup(cmd)
	var ee *exec.ExitError
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return Result{}, fmt.Errorf("%w: %s after %s: %w", ErrCantRun, strings.Join(argv, " "), timeout, ErrTimeout)
	case errors.As(err, &ee) && ee.Exited():
		return Result{Argv: argv, Code: ee.ExitCode(), Output: out.Bytes(), Stdout: stdout.Bytes()}, nil
	case err != nil:
		return Result{}, fmt.Errorf("%w: %s: %w", ErrCantRun, strings.Join(argv, " "), err)
	}
	return Result{Argv: argv, Output: out.Bytes(), Stdout: stdout.Bytes()}, nil
}

// locked is w written by one goroutine at a time.
type locked struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *locked) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
