package port

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// ExecTimeout bounds each command Exec runs: a hung herdr, git, gh or bd must not hang bs.
const ExecTimeout = 60 * time.Second

// Exec is the machine's Runner: each argv run as a child of bs, in dir, with bs's env, in
// its own process group, killed with that group past ExecTimeout (motif 15).
type Exec struct{}

func (Exec) Run(ctx context.Context, dir string, argv ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ExecTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &stdout, &stderr
	Group(cmd)
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%s: no answer within %s", strings.Join(argv, " "), ExecTimeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.Exited() {
		return stdout.Bytes(), &ExitError{Argv: argv, Code: ee.ExitCode(), Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	}
	return stdout.Bytes(), err
}

// Group puts cmd in its own process group and kills that whole group when cmd's context
// ends: a child that forked a grandchild holding the pipe must not outlive the bound
// (motif 15).
func Group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
}
