package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/proc"
)

// launch writes the start's files, starts the harness in the recorded pane, and confirms
// the start (ADR-0010): a watch polls the pane from before herdr is asked and records the
// start at first sight, with its baseline, ahead of the doorbell's first tool calls and
// of the worker's first bs call, which would otherwise heal it as late. herdr's
// agent_pane_busy (a new pane's shell not at its prompt) is retried, since herdr typed
// nothing (bees-12.33); any other herdr error still looks for the harness for
// Timing.AfterErr, since herdr can give up while it runs (bees-12.49, adopted; silently when
// it only gave up waiting for ready, bees-13.78.3). replaces is
// the dead start a resume restarts, "" on a first start.
func (c *Core) launch(ctx context.Context, sp logbook.Spawn, l port.Launch, replaces logbook.ID) error {
	next := bsname.Cmd + " end --incomplete " + sp.Name
	args, err := c.writeFiles(sp.Name, l)
	if err != nil {
		return exit(ExitStart, next, err, "%s: couldn't write its start files", sp.Name)
	}
	if err := herdr.CheckAgentArgs(args); err != nil {
		return exit(ExitStart, next, err, "%s: the launch can't be typed into a pane", sp.Name)
	}
	// A harness already in the pane: this worker's own is adopted; any other would get a
	// twin beside it (motif 7). A pane bs can't look into starts nothing.
	switch p, ok, err := c.own(ctx, sp); {
	case err != nil:
		return exit(ExitStart, next, err, "%s: can't tell whether pane %s already runs %s", sp.Name, sp.Herdr.Pane, c.Harness.Kind())
	case ok:
		if _, _, err := c.recordStart(ctx, sp, p, false, replaces); err != nil {
			return exit(ExitStart, next, err, "%s: its start (pid %d) couldn't be recorded", sp.Name, p.PID)
		}
		return nil
	}
	switch p, ok, err := c.harnessIn(ctx, sp); {
	case err != nil:
		return exit(ExitStart, next, err, "%s: can't tell whether pane %s already runs %s", sp.Name, sp.Herdr.Pane, c.Harness.Kind())
	case ok:
		return exit(ExitStart, next, nil, "%s: pane %s already runs %s (pid %d), which bs has no start of: a second would be its twin", sp.Name, sp.Herdr.Pane, c.Harness.Kind(), p.PID)
	}
	t := c.timing()
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	seen := make(chan sighting, 1)
	go func() {
		got := c.watch(wctx, sp, time.Now().Add(t.Confirm))
		if got.pid != 0 {
			// A process with --name sp.Name in sp's pane is this start: herdr's answer can't undo it.
			st := got.start(sp)
			st.Replaces = replaces
			_, _, got.err = c.Logbook.RecordStartOnce(context.WithoutCancel(ctx), st)
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
		return exit(ExitStart, next, startErr, "%s: herdr started nothing", sp.Name)
	case startErr != nil:
		timer := time.AfterFunc(t.AfterErr, cancel)
		defer timer.Stop()
	}
	got := <-seen
	switch {
	case got.pid == 0 && got.err != nil: // never a clean look: a miss it can't claim (Rule G)
		return exit(ExitCantCheck, bsname.Cmd+" status, then "+next, errors.Join(startErr, got.err), "%s: can't look for its harness in pane %s, so its start is unconfirmed", sp.Name, sp.Herdr.Pane)
	case got.pid == 0:
		return exit(ExitStart, next, startErr, "%s: start unconfirmed: no %s named %s in pane %s", sp.Name, c.Harness.Kind(), sp.Name, sp.Herdr.Pane)
	case got.err != nil:
		return exit(ExitStart, next, got.err, "%s: its start (pid %d) couldn't be recorded", sp.Name, got.pid)
	case errors.Is(startErr, herdr.ErrUnready):
		// The normal path: a harness handed its first prompt is busy at once, so herdr never
		// sees it ready; the watch is bs's confirmation (bees-13.78.3).
	case errors.Is(startErr, herdr.ErrBlocked):
		c.warn("%s started (pid %d) but is blocked at a prompt, such as folder trust: answer it in its pane: herdr --session %s pane read %s --source recent", sp.Name, got.pid, sp.Herdr.Session, sp.Herdr.Pane)
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

// foreground is sp's recorded pane's foreground processes and the harness kind sp was
// started with. A pane herdr no longer knows holds none.
func (c *Core) foreground(ctx context.Context, sp logbook.Spawn) ([]herdr.PaneProcess, string, error) {
	kind, err := c.kindOf(sp)
	if err != nil {
		return nil, "", err
	}
	pi, err := c.herdr(sp.Herdr.Session).ProcessInfo(ctx, sp.Herdr.Pane)
	if errors.Is(err, herdr.ErrPaneGone) {
		return nil, kind, nil
	}
	return pi.Foreground, kind, err
}

// own finds sp's own harness in its recorded pane: a foreground process of the harness's
// kind with --name sp.Name in its argv, as every launch of it has.
func (c *Core) own(ctx context.Context, sp logbook.Spawn) (proc.Process, bool, error) {
	fg, kind, err := c.foreground(ctx, sp)
	if err != nil {
		return proc.Process{}, false, err
	}
	for _, p := range fg {
		if !isOwn(p, kind, sp.Name) {
			continue
		}
		start, err := c.Proc.Start(p.PID)
		if errors.Is(err, proc.ErrGone) {
			continue
		}
		return proc.Process{PID: p.PID, Start: start}, err == nil, err
	}
	return proc.Process{}, false, nil
}

// harnessIn finds any foreground process of the harness's kind in sp's pane.
func (c *Core) harnessIn(ctx context.Context, sp logbook.Spawn) (proc.Process, bool, error) {
	fg, kind, err := c.foreground(ctx, sp)
	if err != nil {
		return proc.Process{}, false, err
	}
	for _, p := range fg {
		if p.Name == kind {
			return proc.Process{PID: p.PID}, true, nil
		}
	}
	return proc.Process{}, false, nil
}

// isOwn reports whether f is worker name's harness: a process of kind with --name name
// in its argv.
func isOwn(f herdr.PaneProcess, kind, name string) bool {
	return f.Name == kind && named(f.Argv, name)
}

// named reports whether argv carries --name name.
func named(argv []string, name string) bool {
	i := slices.Index(argv, "--name")
	return i >= 0 && i+1 < len(argv) && argv[i+1] == name
}

// recordStart records p, found already running, as sp's start; late marks a heal, and
// replaces names the dead start a resume restarts. What it began with is unknown: work it
// started before now would read as its own, so the start records no baseline, and the gate
// fails closed on it (exit 6, naming --force).
func (c *Core) recordStart(ctx context.Context, sp logbook.Spawn, p proc.Process, late bool, replaces logbook.ID) (logbook.Start, bool, error) {
	s := sighting{pid: p.PID, process: p, walkErr: errFoundRunning}
	s.exe, _ = c.Proc.Exe(p.PID)
	st := s.start(sp)
	st.Late, st.Replaces = late, replaces
	return c.Logbook.RecordStartOnce(ctx, st)
}

var errFoundRunning = errors.New("found already running, not seen starting: the processes it began with are unknown")

// StartOf is open spawn sp's newest start. A spawn with none, whose start confirmation
// missed it, is healed (ADR-0010): when its recorded pane runs the harness with --name
// sp.Name in its argv, that process is recorded as its start, marked late. A start whose
// process died counts as none (a worker restarted by hand in its pane, bees-13.9): the
// harness found there is recorded as replacing it; with none found the dead start stands.
// A heal whose probe fails is an error and writes nothing; one that finds nothing for a
// spawn with no start is ok false.
func (c *Core) StartOf(ctx context.Context, sp logbook.Spawn) (logbook.Start, bool, error) {
	st, ok, err := c.Logbook.LatestStart(ctx, sp.ID)
	// A spawn the old bs started is never healed: a harness in its pane is the old bs's
	// live worker, which this bs must neither adopt nor tear down (bees-13.9 SPEC7).
	if err != nil || sp.Ended || sp.Legacy() {
		return st, ok, err
	}
	var replaces logbook.ID
	if ok {
		if alive, err := c.Proc.Alive(st.Process); err != nil || alive {
			return st, true, nil // a lookup that fails is the caller's to report, by its own Alive
		}
		replaces = st.ID
	}
	p, found, err := c.own(ctx, sp)
	if err != nil {
		return st, ok, fmt.Errorf("can't look for %s's harness in pane %s to heal its start: %w", sp.Name, sp.Herdr.Pane, err)
	}
	if !found {
		return st, ok, nil
	}
	healed, recorded, err := c.recordStart(ctx, sp, p, true, replaces)
	if err != nil || !recorded { // another trigger healed it first: its record stands
		return healed, err == nil, err
	}
	if replaces != "" {
		c.warn("%s's recorded process (pid %d) is gone, and pane %s runs it again (pid %d), started outside bs: recorded late", sp.Name, st.PID, sp.Herdr.Pane, p.PID)
	} else {
		c.warn("%s's start was never confirmed; found it running in pane %s (pid %d): recorded late", sp.Name, sp.Herdr.Pane, p.PID)
	}
	return healed, true, nil
}
