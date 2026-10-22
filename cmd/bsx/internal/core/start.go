package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/herdr"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

// launch writes the start's files, starts the harness in the recorded pane, and confirms
// the start (ADR-0010): a watch polls the pane from before herdr is asked and records the
// start at first sight, with its baseline, ahead of the doorbell's first tool calls and
// of the worker's first bs call, which would otherwise heal it as late. herdr's
// agent_pane_busy (a new pane's shell not at its prompt) is retried, since herdr typed
// nothing (bees-12.33); any other herdr error still looks for the harness for
// Timing.AfterErr, since herdr can give up while it runs (bees-12.49, adopted).
func (c *Core) launch(ctx context.Context, sp logbook.Spawn, l port.Launch) error {
	next := "bs end " + sp.Name + " --incomplete"
	args, err := c.writeFiles(sp.Name, l)
	if err != nil {
		return exit(ExitStart, next, err, "bs spawn %s: couldn't write its start files", sp.Name)
	}
	if err := herdr.CheckAgentArgs(args); err != nil {
		return exit(ExitStart, next, err, "bs spawn %s: the launch can't be typed into a pane", sp.Name)
	}
	// A harness already in the pane: this worker's own is adopted; any other would get a
	// twin beside it (motif 7). A pane bs can't look into starts nothing.
	switch p, ok, err := c.own(ctx, sp); {
	case err != nil:
		return exit(ExitStart, next, err, "bs spawn %s: can't tell whether pane %s already runs %s", sp.Name, sp.Herdr.Pane, c.Harness.Kind())
	case ok:
		_, err := c.recordStart(ctx, sp, p, false)
		return err
	}
	switch p, ok, err := c.harnessIn(ctx, sp); {
	case err != nil:
		return exit(ExitStart, next, err, "bs spawn %s: can't tell whether pane %s already runs %s", sp.Name, sp.Herdr.Pane, c.Harness.Kind())
	case ok:
		return exit(ExitStart, next, nil, "bs spawn %s: pane %s already runs %s (pid %d), which bs has no start of: a second would be its twin", sp.Name, sp.Herdr.Pane, c.Harness.Kind(), p.PID)
	}
	t := c.timing()
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	seen := make(chan sighting, 1)
	go func() {
		got := c.watch(wctx, sp, time.Now().Add(t.Confirm))
		if got.pid != 0 {
			// A process with --name sp.Name in sp's pane is this start: herdr's answer can't undo it.
			_, _, got.err = c.Logbook.RecordStartOnce(context.WithoutCancel(ctx), got.start(sp))
		}
		seen <- got
	}()
	startErr := c.startAgent(ctx, sp, args)
	switch {
	case errors.Is(startErr, herdr.ErrPaneBusy), errors.Is(startErr, herdr.ErrNameTaken):
		cancel() // herdr typed nothing, or a live agent has the name: no start of ours to find
		if got := <-seen; got.pid != 0 {
			return got.err // seen all the same: recorded
		}
		return exit(ExitStart, next, startErr, "bs spawn %s: herdr started nothing", sp.Name)
	case startErr != nil:
		timer := time.AfterFunc(t.AfterErr, cancel)
		defer timer.Stop()
	}
	got := <-seen
	switch {
	case got.pid == 0:
		return exit(ExitStart, next, errors.Join(startErr, got.err), "bs spawn %s: start unconfirmed: no %s named %s in pane %s", sp.Name, c.Harness.Kind(), sp.Name, sp.Herdr.Pane)
	case got.err != nil:
		return exit(ExitStart, next, got.err, "bs spawn %s: its start (pid %d) couldn't be recorded", sp.Name, got.pid)
	case startErr != nil:
		c.warn("herdr said %v, but %s started all the same (pid %d): adopted", startErr, sp.Name, got.pid)
	}
	return nil
}

// startAgent is StartAgent, tried again while herdr finds the pane busy, within
// Timing.Busy (bees-12.33).
func (c *Core) startAgent(ctx context.Context, sp logbook.Spawn, args []string) error {
	t := c.timing()
	h := c.herdr(sp.Herdr.Session)
	for deadline := time.Now().Add(t.Busy); ; {
		err := h.StartAgent(ctx, sp.Herdr.Pane, sp.Name, c.Harness.Kind(), args...)
		if !errors.Is(err, herdr.ErrPaneBusy) || time.Now().After(deadline) {
			return err
		}
		if err := sleep(ctx, t.Poll); err != nil {
			return err
		}
	}
}

// sighting is the harness a watch saw, with its descendants at that moment; a zero pid
// is none, with the last lookup's error.
type sighting struct {
	pid      int
	process  proc.Process
	exe      string
	baseline []proc.Process
	walkErr  error
	err      error
}

func (s sighting) start(sp logbook.Spawn) logbook.Start {
	st := logbook.Start{Spawn: sp.ID, Name: sp.Name, Process: s.process, Harness: sp.Harness, Exe: s.exe, Baseline: s.baseline}
	if s.walkErr != nil {
		st.BaselineErr = s.walkErr.Error()
	}
	return st
}

// watch polls sp's pane every Timing.Poll until its own harness shows, deadline passes
// or ctx ends. A lookup can fail for a moment (a process exiting mid-read): it looks
// again, and reports the last failure if it never saw one.
func (c *Core) watch(ctx context.Context, sp logbook.Spawn, deadline time.Time) sighting {
	var last error
	for {
		p, ok, err := c.own(ctx, sp)
		switch {
		case err != nil:
			last = err
		case ok:
			s := sighting{pid: p.PID, process: p}
			s.exe, _ = c.Proc.Exe(p.PID) // informational
			s.baseline, s.walkErr = c.Proc.Descendants(p.PID)
			return s
		}
		if time.Now().After(deadline) {
			return sighting{err: last}
		}
		if err := sleep(ctx, c.timing().Poll); err != nil {
			return sighting{err: last}
		}
	}
}

// own finds sp's own harness in its recorded pane: a foreground process of the harness's
// kind with --name sp.Name in its argv, as every launch of it has. A pane herdr no longer
// knows holds none.
func (c *Core) own(ctx context.Context, sp logbook.Spawn) (proc.Process, bool, error) {
	pi, err := c.herdr(sp.Herdr.Session).ProcessInfo(ctx, sp.Herdr.Pane)
	if errors.Is(err, herdr.ErrPaneGone) {
		return proc.Process{}, false, nil
	} else if err != nil {
		return proc.Process{}, false, err
	}
	for _, p := range pi.Foreground {
		if p.Name != c.Harness.Kind() || !named(p.Argv, sp.Name) {
			continue
		}
		start, err := c.Proc.Start(p.PID)
		if errors.Is(err, proc.ErrGone) {
			return proc.Process{}, false, nil
		}
		return proc.Process{PID: p.PID, Start: start}, err == nil, err
	}
	return proc.Process{}, false, nil
}

// harnessIn finds any foreground process of the harness's kind in sp's pane.
func (c *Core) harnessIn(ctx context.Context, sp logbook.Spawn) (proc.Process, bool, error) {
	pi, err := c.herdr(sp.Herdr.Session).ProcessInfo(ctx, sp.Herdr.Pane)
	if errors.Is(err, herdr.ErrPaneGone) {
		return proc.Process{}, false, nil
	} else if err != nil {
		return proc.Process{}, false, err
	}
	for _, p := range pi.Foreground {
		if p.Name == c.Harness.Kind() {
			return proc.Process{PID: p.PID}, true, nil
		}
	}
	return proc.Process{}, false, nil
}

// named reports whether argv carries --name name.
func named(argv []string, name string) bool {
	i := slices.Index(argv, "--name")
	return i >= 0 && i+1 < len(argv) && argv[i+1] == name
}

// recordStart records p as sp's start, reading its baseline now; late marks a heal.
func (c *Core) recordStart(ctx context.Context, sp logbook.Spawn, p proc.Process, late bool) (logbook.Start, error) {
	s := sighting{pid: p.PID, process: p}
	s.exe, _ = c.Proc.Exe(p.PID)
	s.baseline, s.walkErr = c.Proc.Descendants(p.PID)
	st := s.start(sp)
	st.Late = late
	got, _, err := c.Logbook.RecordStartOnce(ctx, st)
	return got, err
}

// StartOf is open spawn sp's newest start. A spawn with none, whose start confirmation
// missed it, is healed (ADR-0010): when its recorded pane runs the harness with --name
// sp.Name in its argv, that process is recorded as its start, marked late. A heal whose
// probe fails is an error and writes nothing; one that finds nothing is ok false.
func (c *Core) StartOf(ctx context.Context, sp logbook.Spawn) (logbook.Start, bool, error) {
	st, ok, err := c.Logbook.LatestStart(ctx, sp.ID)
	if err != nil || ok || sp.Ended || c.Harness == nil {
		return st, ok, err
	}
	p, found, err := c.own(ctx, sp)
	if err != nil {
		return st, false, fmt.Errorf("can't look for %s's harness in pane %s to heal its start: %w", sp.Name, sp.Herdr.Pane, err)
	}
	if !found {
		return st, false, nil
	}
	if st, err = c.recordStart(ctx, sp, p, true); err != nil {
		return st, false, err
	}
	c.warn("%s's start was never confirmed; found it running in pane %s (pid %d): recorded late", sp.Name, sp.Herdr.Pane, p.PID)
	return st, true, nil
}
