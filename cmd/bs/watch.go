package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/bee"
	"github.com/jmmshn/bees/internal/harness/claude"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/project"
)

// startWatcher leaves a bs watch behind for a bee spawn started. Tests swap it out.
var startWatcher = execWatcher

// errTestBinary: under go test, os.Executable is the test binary, and running it as
// "watch" would rerun the whole suite, detached, once per spawn.
var errTestBinary = errors.New("refusing to start a watcher from a test binary")

// execWatcher runs bs watch in its own session and process group, so it outlives bs spawn
// and the shell that ran it (motif 15). It writes to the logbook spawn wrote to, whatever
// the watcher's environment would resolve. Each start gets its own start key.
func execWatcher(p project.Project, name string, spawn logbook.ID, to string) error {
	if testing.Testing() {
		return errTestBinary
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	start := strconv.FormatInt(time.Now().UnixNano(), 36)
	cmd := exec.Command(exe, "watch", "--project", p.Name, name, string(spawn), start, to)
	cmd.Env = append(os.Environ(), "BS_LOGBOOK="+p.Logbook)
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil { // stdio is /dev/null
		return err
	}
	return cmd.Process.Release()
}

func cmdWatch(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 4 {
		fmt.Fprintln(stderr, "usage: bs watch <bee> <spawn-id> <start> <spawner>   (bs spawn starts it)")
		return errUsage
	}
	name, spawn, start, to := pos[0], logbook.ID(pos[1]), pos[2], pos[3]
	p, lb, err := scope(ctx, *proj)
	if err != nil {
		return err
	}
	defer lb.Close()
	return newFleet(bee.Fleet{P: p, LB: lb}).Watch(ctx, name, spawn, start, to, func() {
		// The notice is stored; a failed ring only delays it to to's next bs inbox.
		if s, err := claude.Find(to); err == nil {
			_ = claude.Ring(s, fmt.Sprintf("[bs] %s stopped without reporting done — run: bs inbox", name), claude.Next)
		}
	})
}
