package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
)

// The background-work gate (ADR-0009 amendment 1, item 3): bs done and bs end refuse while
// the worker's recorded harness process has a descendant its start didn't. It is a speed
// bump: a process that detaches escapes the walk.

var (
	// ErrBackground: the worker's harness runs a process its start didn't (exit 5).
	ErrBackground = errors.New("still runs background work")
	// ErrCantCheck: the gate couldn't look, so it fails closed (exit 6, motif 7).
	ErrCantCheck = errors.New("bs can't check for background work")
	// ErrHarnessGone: the recorded harness process has exited, so there is nothing to walk:
	// its children were reparented away. bs end ends the worker; bs done can't check.
	ErrHarnessGone = errors.New("the worker's recorded process has exited")
	// ErrNoStart: the spawn has no started entry (unconfirmed, or from a bs before starts).
	ErrNoStart = errors.New("no start recorded for this spawn (unconfirmed, or started by a bs from before it recorded starts)")
)

// BackgroundError is ErrBackground with what was found.
type BackgroundError struct {
	Name  string
	Procs []proc.Background
}

// A loop or server the worker left running in the background (a wait for a reply with
// &, a dev server) is background work and counts: the gate can't tell one the worker
// still wants from one it forgot. A foreground loop that runs bs done is one of bs's
// ancestors, and bs's own pipeline is its process group: neither counts (bees-12.50).
func (e *BackgroundError) Error() string {
	lines := []string{fmt.Sprintf("%s %s", e.Name, ErrBackground)}
	for _, b := range e.Procs {
		lines = append(lines, "  "+b.String())
	}
	lines = append(lines, "  (a helper you left running in the background counts, a reply-wait loop too: stop it and run this again)")
	return strings.Join(lines, "\n")
}

func (e *BackgroundError) Unwrap() error { return ErrBackground }

// Gate timing: the whole check, and the second look that drops short-lived children (a
// hook or a statusline command is gone by then). Tests shorten them.
var (
	gateDeadline = 5 * time.Second
	secondLook   = time.Second
)

// CheckBackground returns the background work of open spawn sp: each live descendant of
// its newest start's process that the start's baseline doesn't hold, that isn't this bs
// or one of its ancestors, and that is still there secondLook later. Any failure to look
// is ErrCantCheck; a recorded process that has exited is ErrHarnessGone too. The whole
// check shares one gateDeadline (motif 15): past it, a lookup still running is left to
// its own bound and the check fails closed.
func CheckBackground(ctx context.Context, lb *logbook.Logbook, ps Procs, sp logbook.Spawn) ([]proc.Background, error) {
	ctx, cancel := context.WithTimeout(ctx, gateDeadline)
	defer cancel()
	type result struct {
		bg  []proc.Background
		err error
	}
	done := make(chan result, 1)
	go func() {
		bg, err := checkBackground(ctx, lb, ps, sp)
		done <- result{bg, err}
	}()
	select {
	case r := <-done:
		return r.bg, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %s: %w", ErrCantCheck, sp.Name, ctx.Err())
	}
}

func checkBackground(ctx context.Context, lb *logbook.Logbook, ps Procs, sp logbook.Spawn) ([]proc.Background, error) {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s: %w", ErrCantCheck, sp.Name, fmt.Errorf(format, a...))
	}
	alive, known, st, err := SpawnAlive(ctx, lb, ps, sp)
	switch {
	case err != nil:
		return nil, bad("can't tell whether its process runs: %w", err)
	case !known:
		return nil, bad("%w", ErrNoStart)
	case !alive:
		return nil, bad("pid %d: %w", st.PID, ErrHarnessGone)
	case st.BaselineErr != "":
		return nil, bad("its start couldn't read the processes it began with: %s", st.BaselineErr)
	}
	skip, err := ancestors(ps)
	if err != nil {
		return nil, bad("%w", err)
	}
	// This bs's own process group is its pipeline (bs done 2>&1 | tail -3): its siblings
	// are the call itself, not work left running (bees-12.50).
	mine, err := ps.Group(os.Getpid())
	if err != nil {
		return nil, bad("%w", err)
	}
	fresh := func() ([]proc.Process, error) {
		ds, err := ps.Descendants(st.PID)
		if err == nil {
			err = ctx.Err()
		}
		var out []proc.Process
		for _, p := range ds {
			if slices.Contains(st.Baseline, p) || slices.Contains(skip, p.PID) {
				continue
			}
			g, gerr := ps.Group(p.PID)
			if errors.Is(gerr, proc.ErrGone) || gerr == nil && g == mine {
				continue
			}
			if gerr != nil && err == nil {
				err = gerr
			}
			out = append(out, p)
		}
		return out, err
	}
	first, err := fresh()
	if err != nil {
		return nil, bad("%w", err)
	}
	if len(first) == 0 {
		return nil, nil
	}
	select {
	case <-ctx.Done():
		return nil, bad("%w", ctx.Err())
	case <-time.After(secondLook):
	}
	second, err := fresh()
	if err != nil {
		return nil, bad("%w", err)
	}
	var out []proc.Background
	var pids []int
	for _, p := range first {
		if !slices.Contains(second, p) {
			continue
		}
		c, err := ps.Command(p.PID)
		if errors.Is(err, proc.ErrGone) {
			continue
		} else if err != nil {
			return nil, bad("%w", err)
		}
		out = append(out, proc.Background{Process: p, Command: c})
		pids = append(pids, p.PID)
	}
	if len(out) == 0 {
		return nil, nil
	}
	ports, err := ps.Listening(ctx, pids)
	if err != nil {
		return nil, bad("%w", err)
	}
	for i := range out {
		out[i].Ports = ports[out[i].PID]
	}
	return out, nil
}

// ancestors is this bs and every process above it.
func ancestors(ps Procs) ([]int, error) {
	out := []int{os.Getpid()}
	for pid := os.Getpid(); pid > 1; {
		pp, err := ps.Parent(pid)
		if errors.Is(err, proc.ErrGone) {
			break
		} else if err != nil {
			return nil, err
		}
		if pp <= 0 || slices.Contains(out, pp) {
			break
		}
		out, pid = append(out, pp), pp
	}
	return out, nil
}

// gate refuses to end or report sp while it runs background work: a *BackgroundError, or
// an ErrCantCheck error.
func gate(ctx context.Context, lb *logbook.Logbook, ps Procs, sp logbook.Spawn) error {
	bg, err := CheckBackground(ctx, lb, ps, sp)
	if err != nil {
		return err
	}
	if len(bg) > 0 {
		return &BackgroundError{Name: sp.Name, Procs: bg}
	}
	return nil
}
