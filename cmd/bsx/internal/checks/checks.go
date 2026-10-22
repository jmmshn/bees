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
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
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
	Code   int
	Output []byte // stdout and stderr, interleaved
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

// placeholder is a whole-argument placeholder; any other brace in an argument is refused.
var placeholder = regexp.MustCompile(`^\{([a-z][a-z0-9_]*)\}$`)

// Fill returns argv with each whole-argument {key} replaced by vals[key]. argv[0] is
// never a placeholder.
func Fill(argv []string, vals map[string]Value) ([]string, error) {
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
		case v.Ref && !refOK(v.S):
			return nil, fmt.Errorf("%w: argument %d: %s = %q isn't a valid git ref", ErrBadArgv, i, a, v.S)
		}
		out[i] = v.S
	}
	return out, nil
}

// refOK reports whether git check-ref-format accepts s as a branch name.
func refOK(s string) bool {
	return exec.Command("git", "check-ref-format", "--branch", s).Run() == nil
}

// env is the only environment a command gets: PATH, HOME and LANG, as bs has them.
func env() []string {
	var out []string
	for _, k := range []string{"PATH", "HOME", "LANG"} {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// Run fills c's argv from vals and runs it in dir. A non-zero exit is a Result, not an
// error; a refused argv is ErrBadArgv; a command that couldn't start, or ran past its
// timeout (ErrTimeout, its whole process group killed), is ErrCantRun.
func Run(ctx context.Context, c Cmd, dir string, vals map[string]Value) (Result, error) {
	argv, err := Fill(c.Argv, vals)
	if err != nil {
		return Result{}, err
	}
	if dir == "" {
		return Result{}, fmt.Errorf("%w: no recorded dir to run %s in", ErrCantRun, argv[0])
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var out bytes.Buffer
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env(), &out, &out
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The whole group: a child that forked a grandchild holding the pipe must not outlive
	// the bound (motif 15).
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return Result{}, fmt.Errorf("%w: %s after %s: %w", ErrCantRun, strings.Join(argv, " "), timeout, ErrTimeout)
	case errors.As(err, &ee) && ee.Exited():
		return Result{Code: ee.ExitCode(), Output: out.Bytes()}, nil
	case err != nil:
		return Result{}, fmt.Errorf("%w: %s: %w", ErrCantRun, strings.Join(argv, " "), err)
	}
	return Result{Output: out.Bytes()}, nil
}
