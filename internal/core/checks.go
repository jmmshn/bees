package core

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/logbook"
)

// Handed is what a handoff gives besides the record: the worker's only freedom at exit,
// a verdict (ADR-0010). bs finds a pr goal's change itself (Amendment 4).
type Handed struct {
	// PRs is what a done recorded before Amendment 4 named: read for one release
	// (bees-13.60 Q11), never given by a worker.
	PRs    []string
	Answer string
	Report string
}

// check runs one built-in check of sp outside any goal (prune's clean check).
func (c *Core) check(ctx context.Context, sp logbook.Spawn, name string, h Handed) logbook.CheckResult {
	return c.look(sp, h).check(ctx, goal.Goal{}, name)
}

// check runs check name of goal g. The gate is run by its callers, who know self.
func (k *look) check(ctx context.Context, g goal.Goal, name string) logbook.CheckResult {
	r := logbook.CheckResult{Name: name}
	o, detail, err := k.run(ctx, g, name)
	r.Pass, r.Detail, r.Pending = o == passed && err == nil, detail, o == pending && err == nil
	if err != nil {
		r.Err = err.Error()
	}
	return r
}

// stage runs the checks names of e's goal, in order, skipping waived ones and the gate. It
// stops at the first check that can't run, returning the results so far and that one.
func (k *look) stage(ctx context.Context, e logbook.Effective, names []string) ([]logbook.CheckResult, *logbook.CheckResult) {
	var out []logbook.CheckResult
	for _, name := range names {
		if name == goal.PRWorkspaceCommit || name == goal.PRPushed { // a goal frozen before pr_head
			if slices.Contains(names, goal.PRHead) || slices.Contains(e.Waived, goal.PRHead) {
				continue
			}
		}
		if name == goal.NoBackground || slices.Contains(e.Waived, name) {
			continue
		}
		r := k.check(ctx, e.Goal, name)
		if r.Err != "" {
			return out, &r
		}
		out = append(out, r)
	}
	return out, nil
}

func (k *look) run(ctx context.Context, g goal.Goal, name string) (outcome, string, error) {
	c, ws, h := k.c, k.sp.Workspace, k.h
	if isPRCheck(name) {
		return k.prCheck(ctx, g, name)
	}
	switch name {
	case goal.VerdictGiven:
		switch {
		case h.Answer == "":
			return failed, "--answer yes|no is missing", nil
		case h.Answer != "yes" && h.Answer != "no":
			return failed, fmt.Sprintf("--answer must be yes or no (got %q)", h.Answer), nil
		case strings.TrimSpace(h.Report) == "":
			return failed, "--report <text>|- is missing", nil
		}
		return passed, "", nil
	case goal.NoteChanged:
		return k.noteChanged(ctx)
	case goal.BeadCreated:
		return k.beadCreated(ctx)
	case goal.Clean:
		// Not c.git: trimming would cut the first line's status column.
		b, err := c.Run.Run(ctx, ws.Dir, "git", "-C", ws.Dir, "status", "--porcelain")
		if err != nil {
			return failed, "", err
		}
		if out := string(b); out != "" {
			return failed, "uncommitted or untracked files in " + ws.Dir + ": " + porcelainPaths(out), nil
		}
		return passed, "", nil
	case goal.BranchAhead:
		base, err := c.base(ctx, ws)
		if err != nil {
			return failed, "", err
		}
		out, err := c.git(ctx, ws.Dir, "rev-list", "--count", base+"..refs/heads/"+ws.Branch)
		if err != nil {
			return failed, "", err
		}
		n, err := strconv.Atoi(out)
		if err != nil {
			return failed, "", fmt.Errorf("git rev-list --count: %q", out)
		}
		if n == 0 {
			return failed, fmt.Sprintf("%s has no commit that %s lacks", ws.Branch, short(base)), nil
		}
		if n == 1 {
			return passed, "1 commit ahead of " + short(base), nil
		}
		return passed, fmt.Sprintf("%d commits ahead of %s", n, short(base)), nil
	case goal.BranchMerged:
		ok, why, err := c.merged(ctx, ws)
		return of(ok), why, err
	}
	return failed, "", fmt.Errorf("no check %s", name)
}

// frozen is the bead as spawn froze it. bs goal refuses note and beads on a spawn without
// one, so none here is a broken record, never "unchanged".
func (k *look) frozen() (*logbook.BeadAt, error) {
	if k.sp.At == nil {
		return nil, fmt.Errorf("spawn %s recorded no bead_at", k.sp.ID)
	}
	return k.sp.At, nil
}

// noteChanged reports whether the bead's notes or design differ from spawn's hash.
func (k *look) noteChanged(ctx context.Context) (outcome, string, error) {
	at, err := k.frozen()
	if err != nil {
		return failed, "", err
	}
	id := k.sp.Bead
	b, err := k.c.Beads.Show(ctx, id)
	if err != nil {
		return failed, "", err
	}
	if noteHash(b) == at.Note {
		return failed, fmt.Sprintf("%s's notes and design are as they were at spawn (a label or status change doesn't count): bd update %s --append-notes \"<text>\"", id, id), nil
	}
	return passed, fmt.Sprintf("%s's notes or design changed: bd show %s", id, id), nil
}

// beadCreated reports whether a bead under the bead isn't among those spawn found there.
func (k *look) beadCreated(ctx context.Context) (outcome, string, error) {
	at, err := k.frozen()
	if err != nil {
		return failed, "", err
	}
	kids, err := k.c.Beads.Children(ctx, k.sp.Bead)
	if err != nil {
		return failed, "", err
	}
	kids = slices.DeleteFunc(kids, func(id string) bool { return slices.Contains(at.Children, id) })
	slices.Sort(kids) // bd lists them in no fixed order
	if len(kids) == 0 {
		return failed, fmt.Sprintf("no bead was created under %s since spawn", k.sp.Bead), nil
	}
	return passed, "created under " + k.sp.Bead + ": " + strings.Join(kids, ", "), nil
}

// porcelainPaths is the paths git status --porcelain listed, the first ten of them.
func porcelainPaths(out string) string {
	var paths []string
	for line := range strings.Lines(out) {
		if len(line) > 3 {
			paths = append(paths, strings.TrimSpace(line[3:]))
		}
	}
	return firstTen(paths)
}

// firstTen is paths, the first ten of them and how many more.
func firstTen(paths []string) string {
	if len(paths) > 10 {
		return strings.Join(paths[:10], ", ") + fmt.Sprintf(" and %d more", len(paths)-10)
	}
	return strings.Join(paths, ", ")
}

// complete reports whether sp's goal is complete now, with the complete checks that
// passed: a landed entry says so; a legacy goal is; any other needs a done that passed,
// and every part not complete at handoff needs its complete checks, over the PRs handed
// off, to pass (waived ones skipped). A check that can't run is an error, never
// "incomplete".
func (c *Core) complete(ctx context.Context, sp logbook.Spawn, e logbook.Effective) (bool, []string, []logbook.CheckResult, error) {
	if ld, ok, err := c.Logbook.LandedOf(ctx, sp.ID); err != nil || ok {
		return ok, ld.Checks, nil, err
	}
	if e.IsLegacy() {
		return true, nil, nil, nil
	}
	// Every goal needs its handoff first: a branch with no commits is trivially "merged".
	d, ok, err := c.Logbook.LatestDone(ctx, sp.ID)
	if err != nil {
		return false, nil, nil, err
	}
	if !ok || !d.Passed() {
		return false, nil, []logbook.CheckResult{{Name: "handoff", Detail: "no " + bsname.Cmd + " done that passed its checks"}}, nil
	}
	var complete []string
	if !e.AtHandoff {
		complete = e.Complete
	}
	rs, cant := c.look(sp, Handed{PRs: d.PRs}).stage(ctx, e, complete)
	if cant != nil {
		return false, nil, nil, fmt.Errorf("%s: %s", cant.Name, cant.Err)
	}
	var passed []string
	var failed []logbook.CheckResult
	for _, r := range rs {
		if r.Pass {
			passed = append(passed, r.Name)
		} else {
			failed = append(failed, r)
		}
	}
	return len(failed) == 0, passed, failed, nil
}
