package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jmmshn/bees/cmd/bsx/internal/goal"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
)

// Handed is what a handoff gives besides the record: the worker's only freedom at exit,
// its PR URLs or a verdict (ADR-0010).
type Handed struct {
	PRs    []string
	Answer string
	Report string
}

// errNotBuilt is a built-in check whose feature lands in a later bead: it can't run,
// which is never a pass (Rule G).
func errNotBuilt(bead string) error { return fmt.Errorf("not built yet (%s)", bead) }

// notBuilt is each built-in check of a later bead.
var notBuilt = map[string]string{
	goal.PRExists: "bees-13.6", goal.PROpen: "bees-13.6", goal.PRNotDraft: "bees-13.6",
	goal.PRWorkspaceCommit: "bees-13.6", goal.PRPushed: "bees-13.6", goal.PRCurrent: "bees-13.6",
	goal.CIGreen: "bees-13.6", goal.PRMerged: "bees-13.6", goal.PRClosed: "bees-13.6",
	goal.PushMoved: "bees-13.6", goal.ReviewComment: "bees-13.6",
	goal.NoteChanged: "bees-13.7", goal.BeadCreated: "bees-13.7",
}

// check runs one built-in check of sp. The gate is run by its callers, who know self.
func (c *Core) check(ctx context.Context, sp logbook.Spawn, name string, h Handed) logbook.CheckResult {
	r := logbook.CheckResult{Name: name}
	pass, detail, err := c.run(ctx, sp, name, h)
	r.Pass, r.Detail = pass && err == nil, detail
	if err != nil {
		r.Err = err.Error()
	}
	return r
}

func (c *Core) run(ctx context.Context, sp logbook.Spawn, name string, h Handed) (bool, string, error) {
	ws := sp.Workspace
	if b, ok := notBuilt[name]; ok {
		return false, "", errNotBuilt(b)
	}
	switch name {
	case goal.VerdictGiven:
		switch {
		case h.Answer != "yes" && h.Answer != "no":
			return false, "--answer yes|no is missing", nil
		case h.Report == "":
			return false, "--report <text>|- is missing", nil
		}
		return true, "", nil
	case goal.Clean:
		// Not c.git: trimming would cut the first line's status column.
		b, err := c.Run.Run(ctx, ws.Dir, "git", "-C", ws.Dir, "status", "--porcelain")
		if err != nil {
			return false, "", err
		}
		if out := string(b); out != "" {
			return false, "uncommitted or untracked files in " + ws.Dir + ": " + porcelainPaths(out), nil
		}
		return true, "", nil
	case goal.BranchAhead:
		out, err := c.git(ctx, ws.Dir, "rev-list", "--count", ws.Base+".."+ws.Branch)
		if err != nil {
			return false, "", err
		}
		n, err := strconv.Atoi(out)
		if err != nil {
			return false, "", fmt.Errorf("git rev-list --count: %q", out)
		}
		if n == 0 {
			return false, fmt.Sprintf("%s has no commit that %s lacks", ws.Branch, ws.Base), nil
		}
		return true, fmt.Sprintf("%d commits ahead of %s", n, ws.Base), nil
	case goal.BranchMerged:
		return c.merged(ctx, ws)
	}
	return false, "", fmt.Errorf("no check %s", name)
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
// and every part not complete at handoff needs its complete checks to pass. A check that can't run
// is an error, never "incomplete".
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
		return false, nil, []logbook.CheckResult{{Name: "handoff", Detail: "no bs done that passed its checks"}}, nil
	}
	var passed []string
	var failed []logbook.CheckResult
	for _, name := range e.Complete() {
		r := c.check(ctx, sp, name, Handed{})
		switch {
		case r.Err != "":
			return false, nil, nil, fmt.Errorf("%s: %s", name, r.Err)
		case r.Pass:
			passed = append(passed, name)
		default:
			failed = append(failed, r)
		}
	}
	return len(failed) == 0, passed, failed, nil
}
