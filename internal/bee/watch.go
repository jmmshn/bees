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
	exitGrace  = 10 * time.Second // bs end removes a bee's workspace before it records the end
	appearWait = 10 * time.Minute // for Claude to register, e.g. past a folder-trust prompt
)

// Watch waits for the bee name's Claude to start under spawn, then for that process to
// exit, and then tells to, unless the bee said done since the spawn or the spawn has
// ended or been superseded (bees-1.38). A Claude that never shows up within appearWait
// (or exits before the first look) is told the same way. It reports whether it told to;
// the caller rings. It is bounded (motif 15): it returns once it has told or decided not
// to, when spawn is no longer the bee's latest open spawn, when the logbook file is gone,
// and on the first error. It never retries.
func (f Fleet) Watch(ctx context.Context, name string, spawn logbook.ID, to string) (bool, error) {
	var p Proc
	for deadline := time.Now().Add(appearWait); ; {
		if live, err := f.watching(ctx, name, spawn); err != nil || !live {
			return false, err
		}
		got, ok, err := f.S.Proc(ctx, name)
		if err != nil {
			return false, err
		}
		if ok {
			p = got
			break
		}
		if time.Now().After(deadline) {
			p = Proc{Name: name, Start: "never"}
			break
		}
		if err := sleep(ctx, watchPoll); err != nil {
			return false, err
		}
	}
	for p.Start != "never" {
		if err := sleep(ctx, watchPoll); err != nil {
			return false, err
		}
		if live, err := f.watching(ctx, name, spawn); err != nil || !live {
			return false, err
		}
		exited, err := f.S.Exited(p)
		if err != nil {
			return false, err
		}
		if exited {
			break
		}
	}
	if err := sleep(ctx, exitGrace); err != nil {
		return false, err
	}
	sp, ok, err := f.LB.LatestSpawn(ctx, name)
	if err != nil || !ok {
		return false, err
	}
	return f.LB.RecordExit(ctx, to, name, spawn, p.Start, exitReport(ctx, sp, p.Start == "never"))
}

// watching reports whether spawn is still the bee's latest open spawn, and fails once
// the logbook file is gone: an open handle would keep reading the unlinked file.
func (f Fleet) watching(ctx context.Context, name string, spawn logbook.ID) (bool, error) {
	if _, err := os.Stat(f.P.Logbook); err != nil {
		return false, err
	}
	sp, ok, err := f.LB.LatestSpawn(ctx, name)
	return ok && sp.ID == spawn && !sp.Ended, err
}

// exitReport tells the queen what the bee left behind and what to do next.
func exitReport(ctx context.Context, sp logbook.Spawn, never bool) string {
	last, err := gitOut(ctx, sp.Repo, "log", "-1", "--format=%h %s", "refs/heads/"+sp.Branch)
	if err != nil {
		last = fmt.Sprintf("unknown (%v)", err)
	}
	var b strings.Builder
	what := "exited without reporting done"
	if never {
		what = fmt.Sprintf("never showed up as a running Claude session within %s of its start", appearWait)
	}
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
