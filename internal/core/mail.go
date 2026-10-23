package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
)

// Queen is project's queen session name.
func Queen(project string) string { return "queen-" + project }

// ErrEnded is a ring for a worker whose spawn has ended: nobody reads its inbox.
var ErrEnded = errors.New("has ended")

// Ring rings session to of this logbook with its unread count, at the process its entry
// recorded and through the harness it recorded: the queen's newest queen entry, else an
// open spawn's start (healed if missed). bs never looks a session up by name in herdr or
// the harness. A session whose process is gone is port.ErrNotRunning, one whose harness
// can't ring port.ErrNoRing: either way the message waits for its next bs inbox (motif
// 10). An ended worker is ErrEnded.
func (c *Core) Ring(ctx context.Context, from, to string, p port.Priority) error {
	t := port.Target{Name: to}
	var harness string
	if q, ok, err := c.Logbook.LatestQueen(ctx, to); err != nil {
		return err
	} else if ok {
		t.Session, t.Pane, t.PID, harness = q.Session, q.Pane, q.PID, q.Harness
		if err := c.running(q.Process); err != nil {
			return err
		}
	} else {
		sp, ok, err := c.Logbook.LatestSpawn(ctx, to)
		switch {
		case err != nil:
			return err
		case !ok:
			return fmt.Errorf("%s: %w", to, port.ErrNotRunning)
		case sp.Ended:
			return fmt.Errorf("%s %w", to, ErrEnded)
		}
		st, ok, err := c.StartOf(ctx, sp)
		switch {
		case err != nil:
			return err
		case !ok:
			return fmt.Errorf("%s has no confirmed start: %w", to, port.ErrNotRunning)
		}
		if err := c.running(st.Process); err != nil {
			return err
		}
		t.Session, t.Pane, t.PID, harness = sp.Herdr.Session, sp.Herdr.Pane, st.PID, sp.Harness
	}
	h := c.harnessOf(harness)
	if h == nil {
		return fmt.Errorf("no adapter for its recorded harness %s: %w", harness, port.ErrNoRing)
	}
	unread, err := c.Logbook.Unread(ctx, to)
	if err != nil {
		return err
	}
	return h.Ring(t, Doorbell(unread, from), p)
}

// running is nil while p runs, else port.ErrNotRunning; a failed lookup is an error.
func (c *Core) running(p logbook.Process) error {
	alive, err := c.Proc.Alive(p)
	switch {
	case err != nil:
		return err
	case !alive:
		return fmt.Errorf("pid %d exited: %w", p.PID, port.ErrNotRunning)
	}
	return nil
}

// Doorbell names who the unread messages are from, never what they say, and how to
// reply to the newest from `from`:
//
//	[bs] 3 unread (queen-bees ×2, bees-1-2) — run: bs inbox   (reply with: bs send queen-bees "...")
func Doorbell(unread []logbook.Message, from string) string {
	reply := from
	projects := map[string]bool{}
	for _, m := range unread {
		projects[m.FromProject] = true
	}
	for _, m := range slices.Backward(unread) {
		if m.From == from {
			if m.FromProject != "" { // cross-project mail: the reply goes queen to queen, in the sender's logbook
				reply = "--project " + m.FromProject + " " + Queen(m.FromProject)
			}
			break
		}
	}
	var senders []string
	count := map[string]int{}
	for _, m := range unread {
		if count[m.From] == 0 {
			senders = append(senders, m.From)
		}
		count[m.From]++
	}
	for i, s := range senders {
		if count[s] > 1 {
			senders[i] = fmt.Sprintf("%s ×%d", s, count[s])
		}
	}
	if len(projects) > 1 { // more than one reply command: bs inbox prints each (bees-12.41)
		return fmt.Sprintf(`[%s] %d unread (%s) — run: %s inbox   (it shows how to reply to each)`, bsname.Cmd, len(unread), strings.Join(senders, ", "), bsname.Cmd)
	}
	return fmt.Sprintf(`[%s] %d unread (%s) — run: %s inbox   (reply with: %s send %s "...")`, bsname.Cmd, len(unread), strings.Join(senders, ", "), bsname.Cmd, bsname.Cmd, reply)
}

// Owner is the project a session name belongs to by the naming rule (ADR-0006): queen-<p>,
// or a worker <p>-<n>[-<m>…]; "" for any other name (a keeper's own session).
func Owner(name string) string {
	if p, ok := strings.CutPrefix(name, "queen-"); ok {
		return p
	}
	p, rest, ok := strings.Cut(name, "-")
	if !ok || p == "" {
		return ""
	}
	for _, n := range strings.Split(rest, "-") {
		if n == "" || strings.Trim(n, "0123456789") != "" {
			return ""
		}
	}
	return p
}

// Addressed checks that to may be written into this logbook, whose dir holds every
// project's: a session another project owns, whose logbook is there, would never read it
// here, so it is refused with nothing stored (bees-12.38, motifs 4 and 11), as is a
// worker whose spawn has ended (motif 11: "stored" would promise a reader). stranger
// reports a name that is neither this project's queen nor one of its spawns: stored, but
// never rung (a ring is a user turn, bs rings only sessions it recorded).
func (c *Core) Addressed(ctx context.Context, to, logbookDir string) (stranger bool, err error) {
	if owner := Owner(to); owner != "" && owner != c.Project {
		if _, err := os.Stat(filepath.Join(logbookDir, owner+".db")); err == nil {
			return false, exit(ExitRefused, bsname.Cmd+" send --project "+owner+" "+to+" \"...\"", nil, "%s is a session of project %s, not %s; nothing stored", to, owner, c.Project)
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, exit(ExitCantCheck, bsname.Cmd+" doctor", err, bsname.Cmd+" can't tell whether project %s has a logbook", owner)
		}
	}
	if to == Queen(c.Project) {
		return false, nil
	}
	sp, ok, err := c.Logbook.LatestSpawn(ctx, to)
	if ok && sp.Ended {
		return false, exit(ExitRefused, bsname.Cmd+" status", nil, "%s has ended: nobody would read it; nothing stored", to)
	}
	if err != nil {
		return false, err
	}
	return !ok, nil
}
