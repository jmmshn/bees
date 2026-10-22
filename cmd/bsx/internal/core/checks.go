package core

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jmmshn/bees/cmd/bsx/internal/bsname"
	"github.com/jmmshn/bees/cmd/bsx/internal/goal"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
)

// Handed is what a handoff gives besides the record: the worker's only freedom at exit,
// its PR URLs or a verdict (ADR-0010).
type Handed struct {
	PRs      []string
	Replaces []string // PRs handed off before that these replace
	Answer   string
	Report   string
}

// errNotBuilt is a built-in check whose feature lands in a later bead: it can't run,
// which is never a pass (Rule G).
func errNotBuilt(bead string) error { return fmt.Errorf("not built yet (%s)", bead) }

// notBuilt is each built-in check of a later bead.
var notBuilt = map[string]string{goal.NoteChanged: "bees-13.7", goal.BeadCreated: "bees-13.7"}

// check runs one built-in check of sp outside any goal part (prune's clean check).
func (c *Core) check(ctx context.Context, sp logbook.Spawn, name string, h Handed) logbook.CheckResult {
	return c.look(sp, h).check(ctx, goal.Part{}, name)
}

// check runs check name of part p. The gate is run by its callers, who know self.
func (k *look) check(ctx context.Context, p goal.Part, name string) logbook.CheckResult {
	r := logbook.CheckResult{Name: name}
	o, detail, err := k.run(ctx, p, name)
	r.Pass, r.Detail, r.Pending = o == passed && err == nil, detail, o == pending && err == nil
	if err != nil {
		r.Err = err.Error()
	}
	return r
}

// stage runs the checks of(part) for every part of e, in order, skipping waived ones and
// the gate: a PR check once per set of PRs it reads, any other once. It stops at the first
// check that can't run, returning the results so far and that one.
func (k *look) stage(ctx context.Context, e logbook.Effective, of func(goal.Part) []string) ([]logbook.CheckResult, *logbook.CheckResult) {
	var out []logbook.CheckResult
	var seen []string
	for _, p := range e.Parts {
		for _, name := range of(p) {
			key := name
			if isPRCheck(name) && p.Type != "pr" {
				key += " " + p.Target
			}
			if name == goal.NoBackground || slices.Contains(e.Waived, name) || slices.Contains(seen, key) {
				continue
			}
			seen = append(seen, key)
			r := k.check(ctx, p, name)
			if r.Err != "" {
				return out, &r
			}
			out = append(out, r)
		}
	}
	return out, nil
}

func (k *look) run(ctx context.Context, p goal.Part, name string) (outcome, string, error) {
	c, ws, h := k.c, k.sp.Workspace, k.h
	if b, ok := notBuilt[name]; ok {
		return failed, "", errNotBuilt(b)
	}
	if isPRCheck(name) {
		return k.prCheck(ctx, p, name)
	}
	switch name {
	case goal.VerdictGiven:
		switch {
		case h.Answer != "yes" && h.Answer != "no":
			return failed, "--answer yes|no is missing", nil
		case h.Report == "":
			return failed, "--report <text>|- is missing", nil
		}
		return passed, "", nil
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

// porcelainPaths is the paths git status --porcelain listed, the first ten of them.
func porcelainPaths(out string) string {
	var paths []string
	for line := range strings.Lines(out) {
		if len(line) > 3 {
			paths = append(paths, strings.TrimSpace(line[3:]))
		}
	}
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
	rs, cant := c.look(sp, Handed{PRs: d.PRs}).stage(ctx, e, func(p goal.Part) []string {
		if p.AtHandoff {
			return nil
		}
		return p.Complete
	})
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
