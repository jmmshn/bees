package bee

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/logbook"
)

var (
	watchPoll  = 2 * time.Second
	exitGrace  = 60 * time.Second // bs end removes a bee's workspace before it records the end
	appearWait = 10 * time.Minute // for Claude to register, e.g. past a folder-trust prompt
	readTries  = 3                // logbook reads before a watcher gives up
)

// Watch watches one start of the bee name under spawn (start is unique to that start)
// and tells to, then calls told, when it stops without saying done (bees-1.38):
//   - its Claude is not running appearWait after the start: told once, then Watch keeps
//     watching, and a Claude that turns up later is watched like any other;
//   - its Claude process exits: told, after exitGrace, and Watch returns.
//
// RecordExit decides whether to tell. Watch is bounded (motif 15): it returns once the
// process has exited, when spawn is no longer the bee's latest open spawn, when the
// logbook file is gone, and on an error (a logbook read is tried readTries times).
func (f Fleet) Watch(ctx context.Context, name string, spawn logbook.ID, start, to string, told func()) error {
	var p Proc
	for deadline, warned := time.Now().Add(appearWait), false; ; {
		if live, err := f.watching(ctx, name, spawn); err != nil || !live {
			return err
		}
		got, ok, err := f.S.Proc(ctx, name)
		if err != nil {
			return err
		}
		if ok {
			p = got
			break
		}
		if !warned && time.Now().After(deadline) {
			if err := f.tell(ctx, name, spawn, to, "never:"+start, fmt.Sprintf("not running %s after spawn", appearWait), told); err != nil {
				return err
			}
			warned = true
		}
		if err := sleep(ctx, watchPoll); err != nil {
			return err
		}
	}
	for {
		if err := sleep(ctx, watchPoll); err != nil {
			return err
		}
		if live, err := f.watching(ctx, name, spawn); err != nil || !live {
			return err
		}
		exited, err := f.S.Exited(p)
		if err != nil {
			return err
		}
		if exited {
			break
		}
	}
	if err := sleep(ctx, exitGrace); err != nil {
		return err
	}
	return f.tell(ctx, name, spawn, to, p.Start, "exited without reporting done", told)
}

// tell records that the bee stopped (what) and calls told if RecordExit recorded it.
func (f Fleet) tell(ctx context.Context, name string, spawn logbook.ID, to, key, what string, told func()) error {
	sp, ok, err := f.latest(ctx, name)
	if err != nil || !ok {
		return err
	}
	recorded, err := f.LB.RecordExit(ctx, to, name, spawn, key, exitReport(ctx, sp, what))
	if recorded {
		told()
	}
	return err
}

// watching reports whether spawn is still the bee's latest open spawn, and fails once
// the logbook file is gone: an open handle would keep reading the unlinked file.
func (f Fleet) watching(ctx context.Context, name string, spawn logbook.ID) (bool, error) {
	if _, err := os.Stat(f.P.Logbook); err != nil {
		return false, err
	}
	sp, ok, err := f.latest(ctx, name)
	return ok && sp.ID == spawn && !sp.Ended, err
}

// latest is LatestSpawn, tried readTries times watchPoll apart.
func (f Fleet) latest(ctx context.Context, name string) (sp logbook.Spawn, ok bool, err error) {
	for range readTries {
		if sp, ok, err = f.LB.LatestSpawn(ctx, name); err == nil {
			return sp, ok, nil
		}
		if serr := sleep(ctx, watchPoll); serr != nil {
			return sp, ok, serr
		}
	}
	return sp, ok, err
}

// exitReport tells the queen what the bee left behind and what to do next.
func exitReport(ctx context.Context, sp logbook.Spawn, what string) string {
	last, err := gitOut(ctx, sp.Repo, "log", "-1", "--format=%h %s", "refs/heads/"+sp.Branch)
	if err != nil {
		last = fmt.Sprintf("unknown (%v)", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s. Last commit on %s: %s.", sp.Name, what, sp.Branch, strings.TrimSpace(last))
	if dirty, err := gitOut(ctx, sp.Worktree, "status", "--porcelain"); err == nil && strings.TrimSpace(dirty) != "" {
		fmt.Fprintf(&b, " Uncommitted changes in %s.", sp.Worktree)
	}
	fmt.Fprintf(&b, " Read its pane (herdr pane read %s), then restart it with: bs spawn %s, or end it with: bs end %s", sp.Pane, sp.Bead, sp.Name)
	return b.String()
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
