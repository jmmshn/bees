package port

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
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
	err := RunGroup(cmd)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%s: no answer within %s", strings.Join(argv, " "), ExecTimeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.Exited() {
		return stdout.Bytes(), &ExitError{Argv: argv, Code: ee.ExitCode(), Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	}
	return stdout.Bytes(), err
}

// RunGroup runs cmd in its own process group and, once cmd exits, kills what is left of
// the group: a child that forked a grandchild must not outlive it, holding its output
// open or running on (motif 15). cmd's context ending kills the whole group too. Its
// output is read through pipes bs owns, so Wait returns when cmd exits and the kill
// closes them; nothing cmd wrote before it exited is lost.
func RunGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var copies sync.WaitGroup
	var ends []*os.File // the write ends, closed in bs once cmd has them
	pipe := func(w io.Writer) (*os.File, error) {
		r, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		ends = append(ends, pw)
		copies.Add(1)
		go func() {
			defer copies.Done()
			_, _ = io.Copy(w, r)
			r.Close()
		}()
		return pw, nil
	}
	defer copies.Wait()
	defer func() {
		for _, pw := range ends {
			pw.Close()
		}
	}()
	same := cmd.Stdout != nil && cmd.Stdout == cmd.Stderr
	for _, w := range []*io.Writer{&cmd.Stdout, &cmd.Stderr} {
		if _, isFile := (*w).(*os.File); *w == nil || isFile {
			continue
		}
		if w == &cmd.Stderr && same {
			*w = cmd.Stdout
			continue
		}
		pw, err := pipe(*w)
		if err != nil {
			return err
		}
		*w = pw
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	for _, pw := range ends {
		pw.Close()
	}
	ends = nil
	err := cmd.Wait()
	if kerr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); kerr != nil && !errors.Is(kerr, syscall.ESRCH) {
		err = errors.Join(err, fmt.Errorf("killing what %s left behind: %w", cmd.Path, kerr))
	}
	return err
}
