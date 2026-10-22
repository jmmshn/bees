package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

// Who is the session calling bs, as identity found it.
type Who struct {
	Name    string
	Queen   bool
	Spawn   logbook.ID // a worker's open spawned entry
	Process proc.Process
}

// ErrOutsidePane is a caller with no HERDR_PANE_ID: tests, scripts, the keeper's shell,
// where --as names the session.
var ErrOutsidePane = errors.New("not in a herdr pane")

// Identify finds the session calling bs (ADR-0010): (HERDR_SESSION, or default when
// unset; HERDR_PANE_ID) → the open entry that recorded that pane, accepted only while its
// recorded process is alive and an ancestor of self, the calling bs. Any BS_* variable in
// env is ignored here; inside a recorded pane the pane decides. A lookup that fails is an
// error, never "nobody" (motif 7).
func (c *Core) Identify(ctx context.Context, env []string, self int) (Who, error) {
	pane, session := Getenv(env, "HERDR_PANE_ID"), cmp.Or(Getenv(env, "HERDR_SESSION"), logbook.DefaultSession)
	if pane == "" {
		return Who{}, ErrOutsidePane
	}
	recs, err := c.Logbook.InPane(ctx, session, pane)
	if err != nil {
		return Who{}, err
	}
	up, err := c.ancestors(self)
	if err != nil {
		return Who{}, exit(ExitCantCheck, "", err, "bs can't walk its own ancestors")
	}
	var found []Who
	var unconfirmed []string
	for _, r := range recs {
		if !r.HasProcess {
			unconfirmed = append(unconfirmed, r.Name)
			continue
		}
		alive, err := c.Proc.Alive(r.Process)
		if err != nil {
			return Who{}, exit(ExitCantCheck, "", err, "bs can't tell whether %s's process runs", r.Name)
		}
		if alive && slices.Contains(up, r.Process.PID) {
			found = append(found, Who{Name: r.Name, Queen: r.Queen, Spawn: r.Spawn, Process: r.Process})
		}
	}
	switch {
	case len(found) == 1:
		return found[0], nil
	case len(found) > 1:
		return Who{}, exit(ExitFail, "", nil, "pane %s on herdr session %s is recorded by %d live sessions", pane, session, len(found))
	case len(unconfirmed) > 0:
		return Who{}, exit(ExitStart, "bs end "+unconfirmed[0], nil, "pane %s holds %s, whose start was never confirmed (the heal arrives with bees-13.4)", pane, strings.Join(unconfirmed, ", "))
	}
	return Who{}, exit(ExitRefused, "", nil, "no live session of project %s is recorded in pane %s on herdr session %s", c.Project, pane, session)
}

// ancestors is self and every process above it.
func (c *Core) ancestors(self int) ([]int, error) {
	out := []int{self}
	for pid := self; pid > 1; {
		pp, err := c.Proc.Parent(pid)
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

// Getenv is key's value in env, the last one set winning, as the process would see it.
func Getenv(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

// ErrBackground is the gate's finding (exit 5): the harness runs a process its start
// didn't see.
var ErrBackground = errors.New("still runs background work")

// ErrNoStart is a spawn with no started entry: unconfirmed, or from before starts were
// recorded.
var ErrNoStart = errors.New("no start recorded (unconfirmed, or started before bs recorded starts)")

// ErrHarnessGone is a recorded harness process that has exited: there is nothing to walk.
var ErrHarnessGone = errors.New("the recorded harness process has exited")

// Background returns sp's background work (ADR-0010's no_background gate): each live
// descendant of its newest start's process the baseline doesn't hold, that isn't self or
// one of its ancestors or in self's process group (bs's own pipeline: bees-12.50, 12.52),
// still there a second look later. Any failure to look is an error: the gate fails
// closed. A process that detaches escapes it: a speed bump.
func (c *Core) Background(ctx context.Context, sp logbook.Spawn, self int) ([]proc.Background, error) {
	st, ok, err := c.Logbook.LatestStart(ctx, sp.ID)
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
		return out, nil
	}
	first, err := fresh()
	if err != nil || len(first) == 0 {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(c.timing().SecondLook):
	}
	second, err := fresh()
	if err != nil {
		return nil, err
	}
	var out []proc.Background
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
	}
	return out, nil
}
