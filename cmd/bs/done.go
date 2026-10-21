package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/worker"
)

// Exit codes of bs done and bs end when the background-work gate refuses (ADR-0009
// amendment 1).
const (
	exitBackground = 5 // the worker's harness still runs a process its start didn't
	exitCantCheck  = 6 // bs couldn't look, so it refused (fail closed)
)

// errNotInPane: bs done runs only in a worker's recorded herdr pane.
var errNotInPane = errors.New("bs done runs only inside a worker's session, in the herdr pane bs spawn recorded for it")

// cmdDone reports the caller's bead done to its queen: a done entry, rung like a send. It
// refuses while the worker runs background work or bs can't check, unless --force, which
// records what ran or why bs couldn't look.
func cmdDone(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("done", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (only the pane's own)")
	pr := fs.String("pr", "", "the URL of the PR that delivers the bead")
	report := fs.String("report", "", `what was delivered and where, when there is no PR ("-": read it from stdin)`)
	force := fs.Bool("force", false, "report even while background work runs, listing it in the entry")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 || (*pr == "") == (*report == "") {
		fmt.Fprintln(stderr, "usage: bs done (--pr <url> | --report <text | ->) [--force]")
		return errUsage
	}
	if *report == "-" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		if *report = strings.TrimRight(string(b), "\n"); *report == "" {
			fmt.Fprintln(stderr, "bs done: --report -: stdin was empty")
			return errUsage
		}
	}
	c, err := whoCalls(ctx, stderr) // the pane names the worker: no --as, no BS_SESSION
	if err != nil {
		return err
	}
	if !c.inPane || c.Queen {
		return errNotInPane
	}
	name := c.Name
	p, lb, err := scope(ctx, "done", *proj, "", nil, stderr)
	if err != nil {
		return err
	}
	defer lb.Close()
	sp, ok, err := lb.LatestSpawn(ctx, name)
	if err != nil {
		return err
	}
	if !ok || sp.Ended {
		return fmt.Errorf("%s has no open spawn in project %s: %w", name, p.Name, errNotInPane)
	}
	d := logbook.Done{Spawn: sp.ID, Name: name, Queen: p.Queen(), Bead: sp.Bead, PR: *pr, Report: *report}
	bg, err := worker.CheckBackground(ctx, lb, newProcs(), sp)
	switch {
	case err != nil && !*force:
		fmt.Fprintf(stderr, "bs done: %v\nbs done --force reports anyway, telling %s that bs couldn't check\n", err, d.Queen)
		return exitCode(exitCantCheck)
	case err != nil: // a report never dead-ends (motif 5); the queen reads that it went unchecked
		d.Unchecked = err.Error()
	case len(bg) > 0 && !*force:
		fmt.Fprintf(stderr, "bs done: %v\nstop it and run bs done again; or bs done --force reports anyway, listing it for %s\n", &worker.BackgroundError{Name: name, Procs: bg}, d.Queen)
		return exitCode(exitBackground)
	}
	d.Background = bg
	id, err := lb.RecordDone(ctx, d)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "done %s: %s recorded for %s\n", id, d.Bead, d.Queen)
	return ring(ctx, lb, name, d.Queen, false, stdout, stderr)
}
