package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
	"github.com/jmmshn/bees/internal/watch"
)

// ErrBackground is the gate's finding (exit 5): the harness runs a process its start
// didn't see.
var ErrBackground = errors.New("still runs background work")

// ErrNoStart is a spawn with no started entry: unconfirmed, or from before starts were
// recorded.
var ErrNoStart = errors.New("no start recorded (unconfirmed, or started before bs recorded starts)")

// ErrHarnessGone is a recorded harness process that has exited: there is nothing to walk.
var ErrHarnessGone = errors.New("the recorded harness process has exited")

// background is the gate's refusal: name and what it runs. A loop or server left running
// with & counts: the gate can't tell one the worker still wants from one it forgot
// (bees-12.50).
func background(name string, bg []proc.Background) string {
	return fmt.Sprintf("%s %s:\n  %s\n  (a helper left running in the background counts, a reply-wait loop too: stop it and run this again)",
		name, ErrBackground, lines(bg))
}

// Background returns sp's background work (ADR-0010's no_background gate): each live
// descendant of its newest start's process the baseline doesn't hold, that isn't self or
// one of its ancestors or in self's process group (bs's own pipeline: bees-12.50, 12.52),
// still there a second look later, with the TCP ports it listens on. Any failure to look
// is an error: the gate fails closed. The whole check is bounded by Timing.Gate (motif
// 15). A process that detaches escapes it: a speed bump.
func (c *Core) Background(ctx context.Context, sp logbook.Spawn, self int) ([]proc.Background, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timing().Gate)
	defer cancel()
	type result struct {
		bg  []proc.Background
		err error
	}
	done := make(chan result, 1)
	go func() {
		bg, err := c.background(ctx, sp, self)
		done <- result{bg, err}
	}()
	select {
	case r := <-done:
		return r.bg, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("the check ran past %s: %w", c.timing().Gate, ctx.Err())
	}
}

func (c *Core) background(ctx context.Context, sp logbook.Spawn, self int) ([]proc.Background, error) {
	st, ok, err := c.StartOf(ctx, sp)
	switch {
	case err != nil:
		return nil, err
	case !ok:
		return nil, fmt.Errorf("%s: %w", sp.Name, ErrNoStart)
	case st.BaselineErr != "":
		return nil, fmt.Errorf("its start couldn't read the processes it began with: %s", st.BaselineErr)
	}
	switch alive, err := c.Proc.Alive(st.Process); {
	case err != nil:
		return nil, err
	case !alive:
		return nil, fmt.Errorf("pid %d: %w", st.PID, ErrHarnessGone)
	}
	skip, err := c.ancestors(self)
	if err != nil {
		return nil, err
	}
	w, err := c.watcher(sp.Name)
	if err != nil {
		return nil, err
	}
	skip = append(skip, w...)
	mine, err := c.Proc.Group(self)
	if err != nil {
		return nil, err
	}
	fresh := func() ([]proc.Process, error) {
		ds, err := c.Proc.Descendants(st.PID)
		if err != nil {
			return nil, err
		}
		var out []proc.Process
		for _, p := range ds {
			if slices.Contains(st.Baseline, p) || slices.Contains(skip, p.PID) {
				continue
			}
			g, err := c.Proc.Group(p.PID)
			if errors.Is(err, proc.ErrGone) || err == nil && g == mine {
				continue
			} else if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return out, ctx.Err()
	}
	first, err := fresh()
	if err != nil || len(first) == 0 {
		return nil, err
	}
	if err := sleep(ctx, c.timing().SecondLook); err != nil {
		return nil, err
	}
	second, err := fresh()
	if err != nil {
		return nil, err
	}
	var out []proc.Background
	var pids []int
	for _, p := range first {
		if !slices.Contains(second, p) {
			continue
		}
		cmd, err := c.Proc.Command(p.PID)
		if errors.Is(err, proc.ErrGone) {
			continue
		} else if err != nil {
			return nil, err
		}
		out = append(out, proc.Background{Process: p, Command: cmd})
		pids = append(pids, p.PID)
	}
	if len(out) == 0 {
		return nil, nil
	}
	ports, err := c.Proc.Listening(ctx, pids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Ports = ports[out[i].PID]
	}
	return out, nil
}

// watcher is session name's armed mail watcher, bs inbox --wait, and its ancestors (a
// shell its harness wrapped it in): waiting for mail is how an agy worker is woken, not
// background work it left (bees-13.13.10). The watcher is the process holding its lock,
// never one guessed from a command line.
func (c *Core) watcher(name string) ([]int, error) {
	pid, armed, err := watch.Armed(watch.Path(c.CacheDir, name))
	if err != nil || !armed || pid <= 0 {
		return nil, err
	}
	return c.ancestors(pid)
}

func lines[T fmt.Stringer](xs []T) string {
	var s []string
	for _, x := range xs {
		s = append(s, x.String())
	}
	return strings.Join(s, "\n  ")
}
