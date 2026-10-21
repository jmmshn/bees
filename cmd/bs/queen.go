package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"

	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
	"github.com/jmmshn/bees/internal/prompt"
	"github.com/jmmshn/bees/internal/worker"
)

// execAgent replaces bs with the harness binary program, run in dir with env. Tests swap
// it out.
var execAgent = execAgentProcess

func execAgentProcess(dir string, env []string, program string, args ...string) error {
	path, err := exec.LookPath(program)
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	return syscall.Exec(path, append([]string{program}, args...), env)
}

func cmdQueen(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("queen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "usage: bs queen [--project <name>]")
		return errUsage
	}
	p, cfg, err := resolve("queen", *proj, "")
	if err == nil {
		err = p.NeedRepo()
	}
	if err != nil {
		return err
	}
	// Read the delivery fragment first: an unreadable one starts nothing, not even a logbook.
	delivery, err := worker.Delivery(cfg.Delivery, cfg.Guards)
	if err != nil {
		return err
	}
	// Workers find and ring the queen through the pane and process her entry records
	// (ADR-0009), so a queen outside herdr is one no worker can reach.
	pane := os.Getenv("HERDR_PANE_ID")
	if pane == "" {
		fmt.Fprintln(stderr, "bs queen: not in a herdr pane (HERDR_PANE_ID is unset): start the queen in a herdr pane, so her workers can find and ring her")
		return exitCode(2)
	}
	// The queen starts the project, so her bs commands find a logbook.
	_, lb, err := scope(ctx, "queen", *proj, "", stderr)
	if err != nil {
		return err
	}
	defer lb.Close()
	// No BS_SESSION or BS_PROJECT: the queen's bs finds its name from --name and its project
	// from the repo (R25). Drop inherited ones so they can't override either. A BS_LOGBOOK
	// passes on absolute: relative, it would name another file in her repo.
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "BS_SESSION=") || strings.HasPrefix(kv, "BS_PROJECT=") || strings.HasPrefix(kv, "BS_LOGBOOK=")
	})
	if os.Getenv("BS_LOGBOOK") != "" {
		env = append(env, "BS_LOGBOOK="+p.Logbook)
	}
	file, err := prompt.File(prompt.Queen, p.Name, delivery)
	if err != nil {
		return err
	}
	h, err := adapter(cfg, cfg.Queen)
	if err != nil {
		return err
	}
	l, err := h.Prepare(harness.Start{Role: prompt.Queen, Name: p.Queen(), Dir: p.Repo, PromptFile: file})
	if err != nil {
		return err
	}
	if len(l.Files) > 0 {
		return fmt.Errorf("harness %s wants files for the queen, which bs queen doesn't write yet", cfg.Harness)
	}
	// exec keeps this pid and its start time, so the entry records the queen's own process.
	start, err := proc.Start(os.Getpid())
	if err != nil {
		return err
	}
	// A queen recorded before an exec that then failed is harmless: her pid is dead.
	alive := func(q logbook.Queen) (bool, error) {
		up, err := proc.Alive(q.PID, q.Start)
		if err != nil {
			return false, fmt.Errorf("can't tell whether %s is still running (pid %d): %w", q.Name, q.PID, err)
		}
		return up, nil
	}
	switch q, ok, err := lb.RecordQueen(ctx, logbook.Queen{Name: p.Queen(), Harness: cfg.Harness, Pane: pane, PID: os.Getpid(), Start: start}, alive); {
	case err != nil:
		return err
	case !ok:
		fmt.Fprintf(stderr, "bs queen: %s is already running (pid %d, pane %s); a second queen would race her spawns: use that one, or exit it first\n", q.Name, q.PID, q.Pane)
		return exitCode(2)
	}
	if err := lb.Close(); err != nil { // exec keeps open descriptors
		return err
	}
	return execAgent(p.Repo, env, h.Kind(), l.Args...)
}
