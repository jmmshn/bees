package worker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jmmshn/bees/internal/logbook"
)

// Workspace kinds (ADR-0009). A spawn record with no kind is a git worktree.
const (
	GitWorktree = "git_worktree"
	NoWorkspace = "none"
)

// WorkspaceSpec is the [workspace] config: what a worker's workspace is, and the hooks
// bs runs to make and remove it (ADR-0009).
type WorkspaceSpec struct {
	Kind     string // GitWorktree ("" too) or NoWorkspace
	Init     string // shell command run before the agent starts; "" for none
	Finalize string // shell command bs end runs at teardown; "" for none
}

// hookDirty is the exit code a finalize hook uses to say the workspace holds work.
const hookDirty = 3

// hookTimeout bounds a hook; the hook's whole process group is killed past it (motif 15).
var hookTimeout = 5 * time.Minute

// runHook runs command with sh in dir with env added, in its own process group. It
// returns the outcome, the hook's stdout, and an error unless the hook exited 0.
func runHook(ctx context.Context, what, command, dir string, env map[string]string) (logbook.Hook, string, error) {
	if _, err := os.Stat(dir); err != nil { // else exec blames sh: "fork/exec /usr/bin/sh: no such file"
		return logbook.Hook{Exit: -1}, "", fmt.Errorf("%s hook: can't run in %s: %w", what, dir, err)
	}
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &stdout, &stderr
	cmd.Env = os.Environ()
	for _, k := range slices.Sorted(maps.Keys(env)) {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second // a grandchild that escaped the group can't hold the pipes open
	err := cmd.Run()
	h := logbook.Hook{Stderr: firstLine(stderr.String())}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case ctx.Err() == context.DeadlineExceeded:
		h.Exit = -1
		err = fmt.Errorf("%s hook killed after %s", what, hookTimeout)
	case errors.As(err, &ee):
		h.Exit = ee.ExitCode()
		err = fmt.Errorf("%s hook exited %d", what, h.Exit)
	default:
		h.Exit = -1
		err = fmt.Errorf("%s hook: %w", what, err)
	}
	if err != nil && h.Stderr != "" {
		err = fmt.Errorf("%w: %s", err, h.Stderr)
	}
	return h, stdout.String(), err
}

func firstLine(s string) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			return l
		}
	}
	return ""
}
