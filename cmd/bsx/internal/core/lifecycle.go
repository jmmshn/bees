package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/goal"
	"github.com/jmmshn/bees/cmd/bsx/internal/herdr"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

// Done is the handoff (bs done) of worker who, called by process self: the gate first,
// then every handoff check of the frozen goal (waivers skipped). It stops at the first
// check that can't run (exit 6), otherwise reports every failure at once (exit 7), and
// records the done only when all pass, with landed when the goal completes at handoff.
// force waives the gate and nothing else.
func (c *Core) Done(ctx context.Context, who Who, self int, h Handed, force bool) (logbook.Done, error) {
	if who.Queen {
		return logbook.Done{}, exit(ExitRefused, "", nil, "bs done is a worker's handoff; %s is a queen", who.Name)
	}
	sp, err := c.Logbook.Spawn(ctx, who.Spawn)
	if err != nil {
		return logbook.Done{}, err
	}
	e, err := c.Logbook.Goal(ctx, sp)
	if err != nil {
		return logbook.Done{}, err
	}
	d := logbook.Done{Spawn: sp.ID, Name: sp.Name, Queen: sp.By, Bead: sp.Bead, Answer: h.Answer, Report: h.Report}
	if prev, ok, err := c.Logbook.LatestDone(ctx, sp.ID); err != nil {
		return d, err
	} else if ok {
		d.PRs = prev.PRs // handed-off PRs accumulate across bs done calls
	}
	for _, pr := range h.PRs {
		if !slices.Contains(d.PRs, pr) {
			d.PRs = append(d.PRs, pr)
		}
	}
	var failed []string
	for _, name := range e.Handoff() {
		if slices.Contains(e.Waived, name) {
			continue
		}
		if name == goal.NoBackground {
			bg, err := c.Background(ctx, sp, self)
			switch {
			case err != nil && force:
				d.Unchecked = err.Error()
			case err != nil:
				return d, exit(ExitCantCheck, "bs done --force", err, "bs done: can't check %s for background work", sp.Name)
			case len(bg) > 0 && force:
				d.Background = bg
			case len(bg) > 0:
				return d, exit(ExitBackground, "stop it, then bs done again (or bs done --force)", nil, "%s", background(sp.Name, bg))
			}
			d.Checks = append(d.Checks, logbook.CheckResult{Name: name, Pass: true})
			continue
		}
		r := c.check(ctx, sp, name, h)
		if r.Err != "" {
			return d, exit(ExitCantCheck, "", errors.New(r.Err), "bs done: check %s couldn't run", name)
		}
		if !r.Pass {
			failed = append(failed, name+": "+r.Detail)
		}
		d.Checks = append(d.Checks, r)
	}
	if len(failed) > 0 {
		return d, exit(ExitHandoff, "fix it, then bs done again", nil, "bs done: %d handoff checks failed:\n  %s", len(failed), strings.Join(failed, "\n  "))
	}
	if d.ID, err = c.Logbook.RecordDone(ctx, d); err != nil {
		return d, err
	}
	if !slices.ContainsFunc(e.Parts, func(p goal.Part) bool { return !p.AtHandoff }) {
		err = c.Logbook.RecordLanded(ctx, sp.ID, sp.Name, sp.Name, logbook.Landed{By: "done"})
	}
	return d, err
}

// EndOpts are bs end's flags.
type EndOpts struct {
	Force      bool // waive the gate, and nothing else
	Incomplete bool // end an incomplete goal anyway; prune completes it later
}

// End is the completion gate (bs end, ADR-0010): a complete goal lands, then the spawn
// ends; an incomplete one is exit 9 unless o.Incomplete (refused when nothing was handed
// off). It runs the gate, closes the herdr workspace, stops the process, and keeps the
// directory for prune.
func (c *Core) End(ctx context.Context, by, name string, self int, o EndOpts) error {
	sp, ok, err := c.Logbook.LatestSpawn(ctx, name)
	switch {
	case err != nil:
		return err
	case !ok:
		return exit(ExitRefused, "bs status", nil, "bs end: no worker %s", name)
	case sp.Ended:
		return exit(ExitRefused, "bs prune", nil, "bs end: %s has already ended", name)
	}
	e, err := c.Logbook.Goal(ctx, sp)
	if err != nil {
		return err
	}
	done, passed, failed, err := c.complete(ctx, sp, e)
	switch {
	case err != nil:
		return exit(ExitCantCheck, "", err, "bs end %s: can't tell whether its goal is complete", name)
	case !done && !o.Incomplete:
		var why []string
		for _, f := range failed {
			why = append(why, f.Name+": "+f.Detail)
		}
		return exit(ExitIncomplete, "bs end "+name+" --incomplete", nil, "bs end %s: its goal (--goal %s) is not complete:\n  %s", name, e.Goal, strings.Join(why, "\n  "))
	case !done:
		_, handed, err := c.Logbook.LatestDone(ctx, sp.ID)
		if err != nil {
			return err
		}
		_, started, err := c.StartOf(ctx, sp)
		if err != nil {
			return err
		}
		// An unconfirmed spawn never ran a worker that could hand off: --incomplete is its
		// abort (motif 5). Any other spawn needs something handed off first.
		if !handed && started {
			return exit(ExitRefused, "", nil, "bs end %s --incomplete: nothing was handed off; ending it would lose the work's trail", name)
		}
	}
	if !o.Force {
		switch bg, err := c.Background(ctx, sp, self); {
		case errors.Is(err, ErrHarnessGone): // its children were reparented away: nothing to walk
		case errors.Is(err, ErrNoStart): // nothing recorded to walk: an unconfirmed spawn
		case err != nil:
			return exit(ExitCantCheck, "bs end "+name+" --force", err, "bs end %s: can't check for background work", name)
		case len(bg) > 0:
			return exit(ExitBackground, "bs end "+name+" --force", nil, "%s", background(name, bg))
		}
	}
	switch g, err := c.guests(ctx, sp); {
	case err != nil:
		return exit(ExitCantCheck, "bs end "+name, err, "bs end %s: can't tell what else its workspace %s holds; nothing closed", name, sp.Herdr.Workspace)
	case len(g) > 0:
		return exit(ExitRefused, "herdr --session "+sp.Herdr.Session+" pane move <pane> --new-workspace, then bs end "+name, nil,
			"bs end %s: its workspace %s also holds %s, which closing it would end; nothing closed", name, sp.Herdr.Workspace, strings.Join(g, ", "))
	}
	h := c.herdr(sp.Herdr.Session)
	if err := h.CloseWorkspace(ctx, sp.Herdr.Workspace); err != nil {
		return exit(ExitKept, "bs end "+name, err, "bs end %s: herdr couldn't close its workspace; nothing was recorded", name)
	}
	if err := c.stop(ctx, sp); err != nil {
		return exit(ExitFail, "bs end "+name, err, "bs end %s: its workspace is closed but its process wouldn't stop; nothing was recorded", name)
	}
	if err := c.removeFiles(sp.Name); err != nil {
		c.warn("couldn't remove %s's start files: %v", sp.Name, err)
	}
	// landed only for a goal bs checked: a legacy goal has no completion (ADR-0010).
	if done && !e.IsLegacy() {
		if err := c.Logbook.RecordLanded(ctx, sp.ID, sp.Name, by, logbook.Landed{By: "end", Checks: passed}); err != nil {
			return err
		}
	}
	return c.Logbook.RecordEnd(ctx, by, sp.ID, logbook.Ended{Force: o.Force, Incomplete: !done})
}

// guests names the harness processes in sp's herdr workspace outside its own recorded
// pane: another session's pane moved in (herdr pane move gives it a new id, so it is
// never sp's pane). Closing the workspace would end them (bees-12.36, motif 12). A
// process this logbook recorded is named by its session. Any lookup that fails is an
// error, never "none" (motif 7).
func (c *Core) guests(ctx context.Context, sp logbook.Spawn) ([]string, error) {
	h := c.herdr(sp.Herdr.Session)
	panes, err := h.Panes(ctx, sp.Herdr.Workspace)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, pane := range panes {
		if pane == sp.Herdr.Pane {
			continue
		}
		pi, err := h.ProcessInfo(ctx, pane)
		if errors.Is(err, herdr.ErrPaneGone) {
			continue
		} else if err != nil {
			return nil, err
		}
		for _, p := range pi.Foreground {
			if p.Name != c.Harness.Kind() {
				continue
			}
			who, err := c.recordedAs(ctx, p.PID)
			if err != nil {
				return nil, err
			}
			out = append(out, fmt.Sprintf("%s (pid %d, pane %s)", cmp.Or(who, "a "+p.Name+" process"), p.PID, pane))
		}
	}
	return out, nil
}

// recordedAs is the session whose queen entry or open spawn's start recorded pid, if any.
func (c *Core) recordedAs(ctx context.Context, pid int) (string, error) {
	if q, ok, err := c.Logbook.LatestQueen(ctx, Queen(c.Project)); err != nil {
		return "", err
	} else if ok && q.PID == pid {
		return q.Name, nil
	}
	sps, err := c.Logbook.LatestSpawns(ctx)
	if err != nil {
		return "", err
	}
	for _, sp := range sps {
		if st, ok, err := c.Logbook.LatestStart(ctx, sp.ID); err != nil {
			return "", err
		} else if ok && !sp.Ended && st.PID == pid {
			return sp.Name, nil
		}
	}
	return "", nil
}

// removeFiles removes worker name's per-start files, <name>-<stem>-*: a stem starts with
// a letter, so this never takes the files of a worker whose name extends name (bees-12
// and bees-12-3).
func (c *Core) removeFiles(name string) error {
	fs, err := filepath.Glob(filepath.Join(c.CacheDir, name+"-[a-z]*-*"))
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range fs {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// stop ends sp's recorded harness process if it still runs: SIGTERM, then SIGKILL.
func (c *Core) stop(ctx context.Context, sp logbook.Spawn) error {
	st, ok, err := c.StartOf(ctx, sp)
	if err != nil || !ok {
		return err
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		alive, err := c.Proc.Alive(st.Process)
		if err != nil || !alive {
			return err
		}
		if err := c.Proc.Signal(st.Process, sig); errors.Is(err, proc.ErrGone) {
			return nil
		} else if err != nil {
			return err
		}
		for deadline := time.Now().Add(c.timing().Stop); time.Now().Before(deadline); {
			if alive, err := c.Proc.Alive(st.Process); err != nil || !alive {
				return err
			}
			if err := sleep(ctx, c.timing().Poll); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("pid %d outlived SIGKILL", st.PID)
}

// Pruned is what bs prune did with one ended spawn's workspace.
type Pruned struct {
	Name, Dir string
	Removed   bool
	Why       string // why it was kept
	Unknown   bool   // it couldn't check
}

// Prune removes each ended spawn's workspace once its frozen prune rule passes (or it is
// discarded) and it holds no uncommitted or untracked files, completing a goal ended
// --incomplete on the way. It names what it kept and why.
func (c *Core) Prune(ctx context.Context, by string) ([]Pruned, error) {
	sps, err := c.Logbook.LatestSpawns(ctx)
	if err != nil {
		return nil, err
	}
	var out []Pruned
	for _, sp := range sps {
		if !sp.Ended || sp.Workspace.Kind != "git_worktree" || sp.Workspace.Dir == "" {
			continue
		}
		if _, err := os.Stat(sp.Workspace.Dir); os.IsNotExist(err) {
			continue
		}
		out = append(out, c.prune(ctx, by, sp))
	}
	return out, nil
}

func (c *Core) prune(ctx context.Context, by string, sp logbook.Spawn) Pruned {
	p := Pruned{Name: sp.Name, Dir: sp.Workspace.Dir}
	keep := func(unknown bool, format string, a ...any) Pruned {
		p.Why, p.Unknown = fmt.Sprintf(format, a...), unknown
		return p
	}
	e, err := c.Logbook.Goal(ctx, sp)
	if err != nil {
		return keep(true, "%v", err)
	}
	if !e.Discarded {
		if !e.IsLegacy() {
			switch done, passed, _, err := c.complete(ctx, sp, e); {
			case err != nil:
				return keep(true, "can't tell whether its goal is complete: %v", err)
			case done:
				if err := c.Logbook.RecordLanded(ctx, sp.ID, sp.Name, by, logbook.Landed{By: "prune", Checks: passed}); err != nil {
					return keep(true, "%v", err)
				}
			}
		}
		for _, name := range e.Prune() {
			r := c.check(ctx, sp, name, Handed{})
			if r.Err != "" {
				return keep(true, "%s couldn't run: %s", name, r.Err)
			}
			if !r.Pass {
				return keep(false, "%s", r.Detail)
			}
		}
	}
	if r := c.check(ctx, sp, goal.Clean, Handed{}); r.Err != "" {
		return keep(true, "can't check for uncommitted files: %s", r.Err)
	} else if !r.Pass {
		return keep(false, "%s", r.Detail)
	}
	if _, err := c.git(ctx, sp.Workspace.Repo, "worktree", "remove", sp.Workspace.Dir); err != nil {
		return keep(true, "git worktree remove: %v", err)
	}
	p.Removed = true
	return p
}
