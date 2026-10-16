package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/jmmshn/bees/internal/bee"
	"github.com/jmmshn/bees/internal/harness/claude"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/project"
)

// startWatcher leaves a bs watch behind for a bee spawn started, in its own session and
// process group so it outlives bs spawn and the shell that ran it (motif 15). It writes
// to the logbook spawn wrote to, whatever the watcher's environment would resolve.
func startWatcher(p project.Project, name string, spawn logbook.ID, to string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "watch", "--project", p.Name, name, string(spawn), to)
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
	if len(pos) != 3 {
		fmt.Fprintln(stderr, "usage: bs watch <bee> <spawn-id> <spawner>   (bs spawn starts it)")
		return errUsage
	}
	name, spawn, to := pos[0], logbook.ID(pos[1]), pos[2]
	p, lb, err := scope(ctx, *proj)
	if err != nil {
		return err
	}
	defer lb.Close()
	told, err := newFleet(bee.Fleet{P: p, LB: lb}).Watch(ctx, name, spawn, to)
	if err != nil || !told {
		return err
	}
	// The notice is stored; a failed ring only delays it to to's next bs inbox.
	if s, err := claude.Find(to); err == nil {
		_ = claude.Ring(s, fmt.Sprintf("[bs] %s stopped without reporting done — run: bs inbox", name), claude.Next)
	}
	return nil
}
