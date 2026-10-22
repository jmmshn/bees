package core

import (
	"context"
	"errors"
	"fmt"
	"github.com/jmmshn/bees/cmd/bsx/internal/bsname"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/goal"
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
		return logbook.Done{}, exit(ExitRefused, "", nil, bsname.Cmd+" done is a worker's handoff; %s is a queen", who.Name)
	}
	if who.Spawn == "" {
		return logbook.Done{}, exit(ExitRefused, bsname.Cmd+" status", nil, bsname.Cmd+" done: no open spawn for %s", who.Name)
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
			r := logbook.CheckResult{Name: name, Pass: true}
			switch {
			case err != nil && force:
				d.Unchecked = err.Error()
				r = logbook.CheckResult{Name: name, Err: err.Error(), Waived: true}
			case err != nil:
				return d, exit(ExitCantCheck, bsname.Cmd+" done --force", err, bsname.Cmd+" done: can't check %s for background work", sp.Name)
			case len(bg) > 0 && force:
				d.Background = bg
				r = logbook.CheckResult{Name: name, Detail: ErrBackground.Error(), Waived: true}
			case len(bg) > 0:
				return d, exit(ExitBackground, "stop it, then "+bsname.Cmd+" done again (or "+bsname.Cmd+" done --force)", nil, "%s %s:\n  %s", sp.Name, ErrBackground, lines(bg))
			}
			d.Checks = append(d.Checks, r)
			continue
		}
		r := c.check(ctx, sp, name, h)
		if r.Err != "" {
			return d, exit(ExitCantCheck, "", errors.New(r.Err), bsname.Cmd+" done: check %s couldn't run", name)
		}
		if !r.Pass {
			failed = append(failed, name+": "+r.Detail)
		}
		d.Checks = append(d.Checks, r)
	}
	if len(failed) > 0 {
		return d, exit(ExitHandoff, "fix it, then "+bsname.Cmd+" done again", nil, bsname.Cmd+" done: %d handoff checks failed:\n  %s", len(failed), strings.Join(failed, "\n  "))
	}
	var landed *logbook.Landed
	if !slices.ContainsFunc(e.Parts, func(p goal.Part) bool { return !p.AtHandoff }) {
		landed = &logbook.Landed{By: "done"}
	}
	d.ID, err = c.Logbook.RecordDone(ctx, d, landed)
	return d, err
}

func lines[T fmt.Stringer](xs []T) string {
	var s []string
	for _, x := range xs {
		s = append(s, x.String())
	}
	return strings.Join(s, "\n  ")
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
		return exit(ExitRefused, bsname.Cmd+" status", nil, bsname.Cmd+" end: no worker %s", name)
	case sp.Ended:
		return exit(ExitRefused, bsname.Cmd+" prune", nil, bsname.Cmd+" end: %s has already ended", name)
	case sp.Herdr.Session == logbook.DefaultSession:
		// A spawn there (every legacy one) is a real worker of the shared bs: its workspace
		// and pid are not a toy's to close or signal.
		return exit(ExitRefused, "", nil, bsname.Cmd+" end: %s runs on herdr's default session, which is bs's until the swap (bees-13.9); end it with bs", name)
	}
	e, err := c.Logbook.Goal(ctx, sp)
	if err != nil {
		return err
	}
	done, passed, failed, err := c.complete(ctx, sp, e)
	switch {
	case err != nil:
		return exit(ExitCantCheck, "", err, bsname.Cmd+" end %s: can't tell whether its goal is complete", name)
	case !done && !o.Incomplete:
		var why []string
		for _, f := range failed {
			why = append(why, f.Name+": "+f.Detail)
		}
		return exit(ExitIncomplete, bsname.Cmd+" end "+name+" --incomplete", nil, bsname.Cmd+" end %s: its goal (--goal %s) is not complete:\n  %s", name, e.Goal, strings.Join(why, "\n  "))
	case !done:
		_, handed, err := c.Logbook.LatestDone(ctx, sp.ID)
		if err != nil {
			return err
		}
		_, started, err := c.Logbook.LatestStart(ctx, sp.ID)
		if err != nil {
			return err
		}
		// An unconfirmed spawn never ran a worker that could hand off: --incomplete is its
		// abort (motif 5). Any other spawn needs something handed off first.
		if !handed && started {
			return exit(ExitRefused, "", nil, bsname.Cmd+" end %s --incomplete: nothing was handed off; ending it would lose the work's trail", name)
		}
	}
	if !o.Force {
		switch bg, err := c.Background(ctx, sp, self); {
		case errors.Is(err, ErrHarnessGone): // its children were reparented away: nothing to walk
		case errors.Is(err, ErrNoStart): // nothing recorded to walk: an unconfirmed spawn
		case err != nil:
			return exit(ExitCantCheck, bsname.Cmd+" end "+name+" --force", err, bsname.Cmd+" end %s: can't check for background work", name)
		case len(bg) > 0:
			return exit(ExitBackground, bsname.Cmd+" end "+name+" --force", nil, "%s %s:\n  %s", name, ErrBackground, lines(bg))
		}
	}
	h := c.herdr(sp.Herdr.Session)
	if err := h.CloseWorkspace(ctx, sp.Herdr.Workspace); err != nil {
		return exit(ExitKept, bsname.Cmd+" end "+name, err, bsname.Cmd+" end %s: herdr couldn't close its workspace; nothing was recorded", name)
	}
	if err := c.stop(ctx, sp); err != nil {
		return exit(ExitFail, bsname.Cmd+" end "+name, err, bsname.Cmd+" end %s: its workspace is closed but its process wouldn't stop; nothing was recorded", name)
	}
	// landed only for a goal bs checked: a legacy goal has no completion (ADR-0010).
	if done && !e.IsLegacy() {
		if err := c.Logbook.RecordLanded(ctx, sp.ID, sp.Name, by, logbook.Landed{By: "end", Checks: passed}); err != nil {
			return err
		}
	}
	return c.Logbook.RecordEnd(ctx, by, sp.ID, logbook.Ended{Force: o.Force, Incomplete: !done})
}

// stop ends sp's recorded harness process if it still runs: SIGTERM, then SIGKILL.
func (c *Core) stop(ctx context.Context, sp logbook.Spawn) error {
	st, ok, err := c.Logbook.LatestStart(ctx, sp.ID)
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
	Branch    string // the merged branch it deleted with the workspace
	Why       string // why it was kept, or why a removed workspace's branch stays
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
	// A branch proven merged goes with its workspace, or the bead's next spawn finds it.
	// A discarded one stays: its commits are nobody's to delete.
	if !e.Discarded && slices.Contains(e.Prune(), goal.BranchMerged) && sp.Workspace.Branch != "" {
		if err := c.dropBranch(ctx, sp.Workspace.Repo, sp.Workspace.Branch); err != nil {
			p.Why = fmt.Sprintf("its branch %s stays: %v", sp.Workspace.Branch, err)
		} else {
			p.Branch = sp.Workspace.Branch
		}
	}
	return p
}
