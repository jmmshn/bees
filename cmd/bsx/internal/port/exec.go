package port

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// Exec is the machine's Runner: each argv run as a child of bs, in dir, with bs's env.
type Exec struct{}

func (Exec) Run(ctx context.Context, dir string, argv ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.WaitDelay = dir, &stdout, &stderr, time.Second
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.Exited() {
		return stdout.Bytes(), &ExitError{Argv: argv, Code: ee.ExitCode(), Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	}
	return stdout.Bytes(), err
}
