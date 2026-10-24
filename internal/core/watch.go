package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/watch"
)

// Watch is bs inbox --wait (bees-13.13.10): it holds w's watcher lock and reads the
// logbook every Timing.Mail until mail that wasn't unread when it began comes, and
// returns w's unread mail then, marking nothing read. Its exit is the doorbell for the
// code that runs it (omp's bs extension, ADR-0011). Mail already unread at the start is
// the session's to read already. Every other end is a distinct refusal (motif 11): a
// watcher already armed (pid self is never a second one), w ended or its harness gone (it
// never outlives its session), or timeout passed with no new mail (ExitNoMail: its runner
// starts another).
func (c *Core) Watch(ctx context.Context, w Caller, self int, timeout time.Duration) ([]logbook.Message, error) {
	if w.Role != WorkerR && w.Role != QueenR {
		return nil, exit(ExitRefused, "", nil, "%s inbox --wait watches a queen's or worker's mail; %s is neither", bsname.Cmd, w.Name)
	}
	release, held, err := watch.Arm(watch.Path(c.CacheDir, w.Name), self)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, exit(ExitRefused, "", nil, "%s's watcher is already armed (pid %d): nothing to do, it wakes you when mail comes", w.Name, held)
	}
	defer release()
	seen, err := c.Logbook.Unread(ctx, w.Name)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := c.watched(ctx, w); err != nil {
			return nil, err
		}
		ms, err := c.Logbook.Unread(ctx, w.Name)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(ms, func(m logbook.Message) bool {
			return !slices.ContainsFunc(seen, func(s logbook.Message) bool { return s.ID == m.ID })
		}) {
			return ms, nil
		}
		if !time.Now().Before(deadline) {
			return nil, exit(ExitNoMail, "", nil, "no new mail for %s in %s: nothing to do; run it again to keep waiting", w.Name, timeout)
		}
		if err := sleep(ctx, min(c.timing().Mail, time.Until(deadline))); err != nil {
			return nil, err
		}
	}
}

// watched is nil while w is still a session to watch for: a worker's spawn open, and its
// recorded harness running.
func (c *Core) watched(ctx context.Context, w Caller) error {
	if w.Role == WorkerR {
		sp, ok, err := c.Logbook.LatestSpawn(ctx, w.Name)
		switch {
		case err != nil:
			return err
		case !ok || sp.Ended || sp.ID != w.Spawn:
			return exit(ExitRefused, "", nil, "%s has ended: no mail to watch for", w.Name)
		}
	}
	if err := c.running(w.Process); errors.Is(err, port.ErrNotRunning) {
		return exit(ExitRefused, "", nil, "%s's harness isn't running (%v): no mail to watch for", w.Name, err)
	} else if err != nil {
		return fmt.Errorf("can't tell whether %s's harness runs: %w", w.Name, err)
	}
	return nil
}
