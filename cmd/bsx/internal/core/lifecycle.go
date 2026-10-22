package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/goal"
	"github.com/jmmshn/bees/cmd/bsx/internal/herdr"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

// Done is the handoff (bs done) of worker who, called by process self: the PRs it names
// are checked against its goal's repo and added to those handed off before (h.Replaces
// dropped), then the gate, then every handoff check of the frozen goal (waivers skipped).
// It stops at the first check that can't run (exit 6), otherwise reports every failure at
// once (exit 7), and records the done only when all pass, with landed when the goal
// completes at handoff. Only pending left (CI still running, or the host not yet showing
// a push), is exit 10; with
// o.Wait, bs asks the review host again every Timing.CIPoll until CI finishes, then hands
// off or names what failed, and exit 11 past o.Wait (it runs in the caller's foreground:
// no background job of the harness's). o.Force waives the gate and nothing else.
func (c *Core) Done(ctx context.Context, who Who, self int, h Handed, o DoneOpts) (logbook.Done, error) {
	force := o.Force
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
	if e.Discarded {
		return logbook.Done{}, exit(ExitRefused, "bs send "+sp.By+" \"...\"", nil, "bs done: the keeper discarded %s's goal; there is nothing to hand off", sp.Name)
	}
	d := logbook.Done{Spawn: sp.ID, Name: sp.Name, Queen: sp.By, Bead: sp.Bead, Answer: h.Answer, Report: h.Report}
	if prev, ok, err := c.Logbook.LatestDone(ctx, sp.ID); err != nil {
		return d, err
	} else if ok {
		d.PRs = prev.PRs // handed-off PRs accumulate across bs done calls
	}
	if !slices.Contains(e.Types(), "verdict") && (h.Answer != "" || h.Report != "") {
		where := "bs send " + sp.By + " \"...\""
		if slices.Contains(e.Types(), "note") {
			where = "bd update " + sp.Bead + " --append-notes \"...\", then bs done"
		}
		return d, exit(ExitRefused, where, nil, "bs done: --goal %s takes no --answer or --report", e.Goal)
	}
	if d.PRs, err = c.handedPRs(e.Goal, d.PRs, h); err != nil {
		return d, err
	}
	var failed []string
	// The gate first: no_background, implicit in every handoff; again after a --wait,
	// since the worker may have started something meanwhile.
	gate := func() error {
		switch bg, err := c.Background(ctx, sp, self); {
		case err != nil && force:
			d.Unchecked = err.Error()
		case err != nil:
			return exit(ExitCantCheck, "bs done --force", err, "bs done: can't check %s for background work", sp.Name)
		case len(bg) > 0 && force:
			d.Background = bg
		case len(bg) > 0:
			return exit(ExitBackground, "stop it, then bs done again (or bs done --force)", nil, "%s", background(sp.Name, bg))
		}
		return nil
	}
	if err := gate(); err != nil {
		return d, err
	}
	d.Checks = append(d.Checks, logbook.CheckResult{Name: goal.NoBackground, Pass: true})
	deadline, waited := time.Now().Add(o.Wait), false
	lookCtx := ctx
	if o.Wait > 0 {
		// The looks are bounded, the last included, so bs answers before the caller's
		// shell call is cut (motif 15); recording a handoff that passed is not.
		var cancel context.CancelFunc
		lookCtx, cancel = context.WithDeadline(ctx, deadline.Add(waitGrace))
		defer cancel()
	}
	var rs []logbook.CheckResult
	for {
		var cant *logbook.CheckResult
		rs, cant = c.look(sp, Handed{PRs: d.PRs, Answer: h.Answer, Report: h.Report}).stage(lookCtx, e, func(p goal.Part) []string { return p.Handoff })
		if cant != nil && o.Wait > 0 && lookCtx.Err() != nil {
			return d, exit(ExitWaited, "bs done --wait again, or ask the queen", nil, "bs done --wait: still pending after %s (%s ran out of time)", o.Wait, cant.Name)
		}
		if cant != nil {
			return d, exit(ExitCantCheck, "bs done again once it can run", errors.New(cant.Err), "bs done: check %s couldn't run", cant.Name)
		}
		if o.Wait <= 0 || !pendingOnly(rs) {
			break
		}
		left := time.Until(deadline)
		if left <= 0 {
			return d, exit(ExitWaited, "bs done --wait again, or ask the queen", nil, "bs done --wait: still pending after %s:\n  %s", o.Wait, strings.Join(pendingLines(rs), "\n  "))
		}
		c.warn("still pending (%s); asking again in %s", strings.Join(pendingLines(rs), "; "), min(c.timing().CIPoll, left).Round(time.Second))
		if err := sleep(lookCtx, min(c.timing().CIPoll, left)); err != nil {
			return d, err
		}
		waited = true
	}
	if waited {
		if err := gate(); err != nil {
			return d, err
		}
	}
	for _, r := range rs {
		if !r.Pass {
			failed = append(failed, r.Name+": "+r.Detail)
		}
	}
	d.Checks = append(d.Checks, rs...)
	if pendingOnly(rs) {
		return d, exit(ExitPending, "bs done --wait (bs waits for CI, then hands off or names what failed)", nil, "bs done: still pending (CI running, or the host not yet showing a push), and nothing else failed:\n  %s", strings.Join(failed, "\n  "))
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

// waitGrace is how long the last look of a bs done --wait may run past its deadline.
const waitGrace = 45 * time.Second

// DoneOpts are bs done's flags.
type DoneOpts struct {
	Force bool          // waive the gate, and nothing else
	Wait  time.Duration // wait this long while only pending is left; 0 doesn't wait
}

// pendingLines is each pending result of rs, as bs prints it.
func pendingLines(rs []logbook.CheckResult) []string {
	var out []string
	for _, r := range rs {
		if r.Pending {
			out = append(out, r.Name+": "+r.Detail)
		}
	}
	return out
}

// pendingOnly reports whether rs failed, and only on something still pending.
func pendingOnly(rs []logbook.CheckResult) bool {
	failed := slices.DeleteFunc(slices.Clone(rs), func(r logbook.CheckResult) bool { return r.Pass })
	return len(failed) > 0 && !slices.ContainsFunc(failed, func(r logbook.CheckResult) bool { return !r.Pending })
}

// handedPRs is the PRs handed off once h is: prev, less h.Replaces, plus h.PRs, each in
// the one spelling bs records. Only a goal with a pr part takes PRs; each must be a PR of
// that part's repo; a replaced one must have been handed off; and a pr goal needs one
// (Rule G: refused before any check runs).
func (c *Core) handedPRs(g goal.Goal, prev []string, h Handed) ([]string, error) {
	i := slices.IndexFunc(g.Parts, func(p goal.Part) bool { return p.Type == "pr" })
	if i < 0 {
		if len(h.PRs)+len(h.Replaces) > 0 {
			return nil, exit(ExitRefused, "bs done", nil, "bs done: --goal %s takes no --pr or --replaces", g)
		}
		return prev, nil
	}
	repo := g.Parts[i].Repo
	canon := func(u string) (string, error) {
		r, err := c.Review.Parse(u)
		if err != nil {
			return "", exit(ExitRefused, "", err, "bs done")
		}
		if !strings.EqualFold(r.Repo, repo) {
			return "", exit(ExitRefused, "", nil, "bs done: %s is a change of %s, not of this spawn's repo %s", u, r.Repo, repo)
		}
		return r.URL, nil
	}
	out := slices.Clone(prev)
	for _, u := range h.Replaces {
		r, err := canon(u)
		if err != nil {
			return nil, err
		}
		j := slices.Index(out, r)
		if j < 0 {
			return nil, exit(ExitRefused, "", nil, "bs done --replaces %s: it was never handed off (handed off: %s)", r, cmp.Or(strings.Join(out, " "), "none"))
		}
		out = slices.Delete(out, j, j+1)
	}
	for _, u := range h.PRs {
		r, err := canon(u)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, exit(ExitRefused, "bs done --pr <url>", nil, "bs done: --goal %s hands off PRs, and none is handed off", g)
	}
	return out, nil
}

// EndOpts are bs end's flags.
type EndOpts struct {
	Force      bool // waive the gate and the moved-pane refusal (stopping it where it runs), and nothing else
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
	case err != nil && !o.Incomplete:
		return exit(ExitCantCheck, "bs end "+name+" --incomplete", err, "bs end %s: can't tell whether its goal is complete", name)
	case err != nil:
		// --incomplete is the way out while a check can't run (motif 5); prune completes it.
		c.warn("can't tell whether %s's goal is complete (%v): ending it --incomplete", name, err)
		done = false
	}
	switch {
	case !done && e.Discarded: // the keeper's word that the work is wanted nowhere: nothing to wait for
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
	// A pane moved out of its workspace keeps running there: closing the workspace
	// wouldn't stop it, so only --force ends it, by its recorded process (bees-12.46).
	switch pid, err := c.moved(ctx, sp); {
	case err != nil:
		return exit(ExitCantCheck, "bs end "+name, err, "bs end %s: can't tell whether its pane is still in its workspace %s; nothing closed", name, sp.Herdr.Workspace)
	case pid != 0 && !o.Force:
		return exit(ExitRefused, "bs end "+name+" --force (stops pid "+strconv.Itoa(pid)+" wherever it runs)", nil,
			"bs end %s: its pane %s moved out of its workspace %s, and its process (pid %d) still runs there; nothing closed", name, sp.Herdr.Pane, sp.Herdr.Workspace, pid)
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

// moved is the pid of sp's recorded process when it still runs but its recorded pane is
// no longer in its recorded workspace (herdr pane move gives the pane a new id; a moved
// last pane closes the workspace), else 0.
func (c *Core) moved(ctx context.Context, sp logbook.Spawn) (int, error) {
	panes, err := c.herdr(sp.Herdr.Session).Panes(ctx, sp.Herdr.Workspace)
	if err != nil || slices.Contains(panes, sp.Herdr.Pane) {
		return 0, err
	}
	st, ok, err := c.StartOf(ctx, sp)
	if err != nil || !ok {
		return 0, err
	}
	if alive, err := c.Proc.Alive(st.Process); err != nil || !alive {
		return 0, err
	}
	return st.PID, nil
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

// Pruned is what bs prune did with one ended spawn's workspace and branch.
type Pruned struct {
	Name, Dir string
	Removed   bool   // its directory was removed
	Gone      bool   // its directory was gone already
	Branch    string // its branch, when it had one
	Deleted   bool   // its local branch was deleted
	Remote    string // the remote branch of the same name, left alone: remote branches are the keeper's (bees-1.34)
	Held      string // the handed-off change that holds the branch's work, when that released it
	Why       string // why it, or its branch, was kept
	Unknown   bool   // it couldn't check
}

// Prune removes each ended spawn's workspace once its frozen prune rule passes (or it is
// discarded) and it holds no uncommitted or untracked files, then its local branch once
// that is proven merged (or discarded), completing a goal ended --incomplete on the way. A
// workspace whose directory is already gone still has its branch pruned, so nothing is
// kept forever (bees-12.41). It never touches a remote branch, and names what it kept and
// why.
func (c *Core) Prune(ctx context.Context, by string) ([]Pruned, error) {
	sps, err := c.Logbook.LatestSpawns(ctx)
	if err != nil {
		return nil, err
	}
	var out []Pruned
	for _, sp := range sps {
		ws := sp.Workspace
		if !sp.Ended || ws.Kind != "git_worktree" || ws.Dir == "" {
			continue
		}
		_, err := os.Stat(ws.Dir)
		dir := err == nil
		if err != nil && !os.IsNotExist(err) {
			out = append(out, Pruned{Name: sp.Name, Dir: ws.Dir, Branch: ws.Branch, Unknown: true, Why: "can't tell whether its directory exists: " + err.Error()})
			continue
		}
		branch, err := c.hasBranch(ctx, ws)
		switch {
		case err != nil:
			out = append(out, Pruned{Name: sp.Name, Dir: ws.Dir, Branch: ws.Branch, Unknown: true, Why: "can't tell whether its branch exists: " + err.Error()})
		case dir || branch:
			out = append(out, c.prune(ctx, by, sp, dir, branch))
		}
	}
	return out, nil
}

// hasBranch reports whether ws's branch exists in its repo; a workspace with no branch has
// none.
func (c *Core) hasBranch(ctx context.Context, ws logbook.Workspace) (bool, error) {
	if ws.Branch == "" || ws.Repo == "" {
		return false, nil
	}
	_, err := c.git(ctx, ws.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+ws.Branch)
	if isExit(err, 1) {
		return false, nil
	}
	return err == nil, err
}

func (c *Core) prune(ctx context.Context, by string, sp logbook.Spawn, dir, branch bool) Pruned {
	ws := sp.Workspace
	p := Pruned{Name: sp.Name, Dir: ws.Dir, Branch: ws.Branch, Gone: !dir}
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
		d, _, err := c.Logbook.LatestDone(ctx, sp.ID)
		if err != nil {
			return keep(true, "%v", err)
		}
		rs, cant := c.look(sp, Handed{PRs: d.PRs}).stage(ctx, e, func(p goal.Part) []string { return p.Prune })
		if cant != nil {
			return keep(true, "%s couldn't run: %s", cant.Name, cant.Err)
		}
		for _, r := range rs {
			if !r.Pass {
				return keep(false, "%s", r.Detail)
			}
		}
	}
	// An ended worker whose process still runs (a --force that couldn't stop it) may
	// still write there.
	if st, ok, err := c.StartOf(ctx, sp); err != nil {
		return keep(true, "can't tell whether its worker still runs: %v", err)
	} else if ok {
		if alive, err := c.Proc.Alive(st.Process); err != nil {
			return keep(true, "can't tell whether its worker still runs: %v", err)
		} else if alive {
			return keep(false, "its ended worker still runs (pid %d): stop it, then bs prune again", st.PID)
		}
	}
	if dir {
		// Only a directory that is still a worktree of its repo: one taken over or remade
		// by hand is not this spawn's to remove (the pilot's samePath guard, bees-8.4).
		if mine, err := c.isWorktree(ctx, ws); err != nil {
			return keep(true, "can't list %s's worktrees: %v", ws.Repo, err)
		} else if !mine {
			return keep(false, "it is no longer a worktree of %s", ws.Repo)
		}
		if r := c.check(ctx, sp, goal.Clean, Handed{}); r.Err != "" {
			return keep(true, "can't check for uncommitted files: %s", r.Err)
		} else if !r.Pass {
			return keep(false, "%s", r.Detail)
		}
		// git status hides ignored files, and a report in an ignored scratch/ is how five
		// pilot reports were lost (bees-13.7): never remove what nobody has looked at.
		if ign, err := c.ignored(ctx, ws.Dir); err != nil {
			return keep(true, "can't list ignored files: %v", err)
		} else if ign != "" {
			return keep(false, "ignored files in %s: %s; look at them, remove what is wanted nowhere, then bs prune again", ws.Dir, ign)
		}
		if _, err := c.git(ctx, ws.Repo, "worktree", "remove", ws.Dir); err != nil {
			return keep(true, "git worktree remove: %v", err)
		}
		p.Removed = true
	}
	if !branch {
		return p
	}
	// The prune rule's branch_merged, when it ran and passed, is the proof: one gh call,
	// not two. A merged (or closed draft) handed-off change holding the tip is another.
	release := e.Discarded || slices.Contains(e.Prune(), goal.BranchMerged)
	if !release && slices.ContainsFunc(e.Parts, func(p goal.Part) bool { return p.Type == "pr" }) {
		d, _, err := c.Logbook.LatestDone(ctx, sp.ID)
		if err != nil {
			return keep(true, "%v", err)
		}
		if p.Held, err = c.heldByChange(ctx, sp, d.PRs, slices.Contains(e.Prune(), goal.PRClosed)); err != nil {
			return keep(true, "can't tell whether a handed-off change holds branch %s: %v", ws.Branch, err)
		}
		release = p.Held != ""
	}
	if why, err := c.pruneBranch(ctx, ws, release); err != nil {
		return keep(true, "%v", err)
	} else if why != "" {
		return keep(false, "%s", why)
	}
	p.Deleted = true
	switch _, err := c.git(ctx, ws.Repo, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+ws.Branch); {
	case err == nil:
		p.Remote = "origin/" + ws.Branch
	case !isExit(err, 1):
		c.warn("can't tell whether origin/%s exists: %v", ws.Branch, err)
	}
	return p
}

// ignored is the ignored files in dir, as porcelainPaths names them, or "".
func (c *Core) ignored(ctx context.Context, dir string) (string, error) {
	b, err := c.Run.Run(ctx, dir, "git", "-C", dir, "status", "--porcelain", "--ignored")
	if err != nil {
		return "", err
	}
	var ign strings.Builder
	for line := range strings.Lines(string(b)) {
		if strings.HasPrefix(line, "!! ") {
			ign.WriteString(line)
		}
	}
	return porcelainPaths(ign.String()), nil
}

// heldByChange is the URL of one of prs, handed off under a pr goal, that holds sp's
// branch tip (as the VCS reads it) among its commits and is merged (or, with closed,
// closed unmerged), else "": the work is then on the review host, whatever the branch was
// called there, and the local branch holds nothing more. It catches a squash merge of a
// branch pushed under another name, which merged's proofs miss.
func (c *Core) heldByChange(ctx context.Context, sp logbook.Spawn, prs []string, closed bool) (string, error) {
	ws := sp.Workspace
	tip, ok, err := c.VCS.Branch(ctx, ws.Repo, ws.Branch)
	if err != nil || !ok {
		return "", err
	}
	k := c.look(sp, Handed{})
	for _, u := range prs {
		r, err := c.Review.Parse(u)
		if err != nil {
			return "", err
		}
		v, err := k.view(ctx, r)
		if err != nil {
			return "", err
		}
		done := v.State == "merged" || closed && v.State == "closed"
		if done && (v.HeadCommit == tip || slices.Contains(v.Commits, tip)) {
			return v.URL, nil
		}
	}
	return "", nil
}

// pruneBranch deletes ws's local branch once it is proven merged into its base, whatever
// the goal's prune rule (a verdict goal prunes at once, but its branch may hold commits),
// or released: discarded (the keeper's recorded word that the work is wanted nowhere),
// proven by the prune rule's branch_merged, or held by a merged (or closed draft) change.
// It returns why it kept the branch, or err when it couldn't check.
func (c *Core) pruneBranch(ctx context.Context, ws logbook.Workspace, released bool) (string, error) {
	if !released {
		switch ok, why, err := c.merged(ctx, ws); {
		case err != nil:
			return "", fmt.Errorf("can't tell whether branch %s is merged: %w", ws.Branch, err)
		case !ok:
			return fmt.Sprintf("branch %s: %s; delete it by hand once that work is wanted nowhere", ws.Branch, why), nil
		}
	}
	switch at, prunable, err := c.checkedOut(ctx, ws.Repo, ws.Branch); {
	case err != nil:
		return "", fmt.Errorf("can't tell whether a worktree has branch %s in use: %w", ws.Branch, err)
	case at != "" && (!prunable || !samePath(at, ws.Dir)):
		return fmt.Sprintf("branch %s is in use by the worktree %s", ws.Branch, at), nil
	case at != "":
		// Its own directory is gone, and git still counts the branch as checked out there:
		// drop that one entry, never the repo's other stale ones (motif 12).
		if _, err := c.git(ctx, ws.Repo, "worktree", "remove", at); err != nil {
			return "", fmt.Errorf("git worktree remove %s: %w", at, err)
		}
	}
	// -D: a squash or rebase merge leaves the branch unmerged to git; merged proved it.
	if _, err := c.git(ctx, ws.Repo, "branch", "-D", ws.Branch); err != nil {
		return "", fmt.Errorf("git branch -D %s: %w", ws.Branch, err)
	}
	return "", nil
}

// isWorktree reports whether ws.Dir is a worktree git lists for ws.Repo.
func (c *Core) isWorktree(ctx context.Context, ws logbook.Workspace) (bool, error) {
	out, err := c.git(ctx, ws.Repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, err
	}
	for f := range strings.SplitSeq(out, "\x00") {
		if at, ok := strings.CutPrefix(f, "worktree "); ok && samePath(at, ws.Dir) {
			return true, nil
		}
	}
	return false, nil
}

// samePath reports whether a and b name the same directory, symlinks resolved (motif 3),
// even once it is gone: git lists a removed worktree by its resolved path (/private/var on
// macOS), the record by the path bs was given.
func samePath(a, b string) bool { return resolved(a) == resolved(b) }

// resolved is path with symlinks resolved in its longest existing prefix.
func resolved(path string) string {
	path = filepath.Clean(path)
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	dir, base := filepath.Split(path)
	if dir = filepath.Clean(dir); dir == path {
		return path
	}
	return filepath.Join(resolved(dir), base)
}
