package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/prompt"
)

// Check runs worker name's handoff checks now, over the PRs it handed off, and writes
// nothing (bs check): the queen runs it before merging. It returns every result so far;
// the error is exit 6 at the first check that couldn't run, else exit 7 when one failed.
func (c *Core) Check(ctx context.Context, name string, self int) ([]logbook.CheckResult, error) {
	sp, ok, err := c.Logbook.LatestSpawn(ctx, name)
	switch {
	case err != nil:
		return nil, err
	case !ok:
		return nil, exit(ExitRefused, bsname.Cmd+" status", nil, "no worker %s", name)
	}
	e, err := c.Logbook.Goal(ctx, sp)
	if err != nil {
		return nil, err
	}
	d, _, err := c.Logbook.LatestDone(ctx, sp.ID)
	if err != nil {
		return nil, err
	}
	gate := logbook.CheckResult{Name: goal.NoBackground, Pass: true}
	switch bg, err := c.Background(ctx, sp, self); {
	case errors.Is(err, ErrHarnessGone), errors.Is(err, ErrNoStart):
		gate.Detail = "its harness isn't running"
	case err != nil:
		gate.Pass, gate.Err = false, err.Error()
		return []logbook.CheckResult{gate}, exit(ExitCantCheck, "", err, "%s: the gate couldn't run", name)
	case len(bg) > 0:
		gate.Pass, gate.Detail = false, fmt.Sprintf("%d background processes: %s", len(bg), lines(bg))
	}
	rs, cant := c.look(sp, Handed{PRs: d.PRs, Answer: d.Answer, Report: d.Report}).stage(ctx, e, func(p goal.Part) []string { return p.Handoff })
	rs = append([]logbook.CheckResult{gate}, rs...)
	if cant != nil {
		return append(rs, *cant), exit(ExitCantCheck, "", errors.New(cant.Err), "%s: %s couldn't run", name, cant.Name)
	}
	if pendingOnly(rs) {
		return rs, exit(ExitPending, bsname.Cmd+" check "+name+" again once CI finishes", nil, "%s: still pending (CI running, or the host not yet showing a push), and nothing else failed", name)
	}
	if slices.ContainsFunc(rs, func(r logbook.CheckResult) bool { return !r.Pass }) {
		return rs, exit(ExitHandoff, "", nil, "%s: its handoff doesn't hold", name)
	}
	return rs, nil
}

// GoalReq is a bs goal call: an amend (Values), a waiver (Waive), or the keeper's discard.
type GoalReq struct {
	By     string // who records it
	Self   int    // the calling bs: its ancestors say whether a worker or a queen runs it
	Name   string
	Values []string // the new --goal values; ["discard"] is a discard
	Waive  string
}

// Goal amends worker name's live, incomplete goal, waives one of its checks, or discards
// it (ADR-0010). The queen or the keeper may amend or waive, never a worker or anything it
// started; only the keeper, with no recorded queen or worker among its ancestors, may
// discard, at any time. An amend re-runs the spawn-time refusals for the new goal, and the
// worker is told its new goal.
func (c *Core) Goal(ctx context.Context, req GoalReq) (logbook.Effective, error) {
	discard := slices.Equal(req.Values, []string{"discard"})
	what := req.Name
	sp, ok, err := c.Logbook.LatestSpawn(ctx, req.Name)
	switch {
	case err != nil:
		return logbook.Effective{}, err
	case !ok:
		return logbook.Effective{}, exit(ExitRefused, bsname.Cmd+" status", nil, "no worker %s", req.Name)
	case (len(req.Values) > 0) == (req.Waive != ""):
		return logbook.Effective{}, exit(ExitRefused, "", nil, "%s: give the new goal (--goal values, or discard) or --waive <check>, one of them", what)
	}
	up, err := c.recordedAbove(ctx, req.Self)
	if err != nil {
		return logbook.Effective{}, exit(ExitCantCheck, "", err, "%s: can't tell who runs it", what)
	}
	for _, s := range up {
		switch {
		case s != Queen(c.Project):
			return logbook.Effective{}, exit(ExitRefused, bsname.Cmd+" send "+sp.By+" \"...\"", nil, "%s: run by worker %s or something it started; a worker never changes a goal", what, s)
		case discard:
			return logbook.Effective{}, exit(ExitRefused, "ask the keeper", nil, "%s discard: only the keeper discards a goal, from a shell no queen or worker started (%s runs this)", what, s)
		}
	}
	e, err := c.Logbook.Goal(ctx, sp)
	if err != nil {
		return logbook.Effective{}, err
	}
	if !discard {
		switch _, landed, err := c.Logbook.LandedOf(ctx, sp.ID); {
		case err != nil:
			return logbook.Effective{}, err
		case landed:
			return logbook.Effective{}, exit(ExitRefused, "", nil, "%s: its goal is complete (landed); there is nothing left to change", what)
		case sp.Ended:
			return logbook.Effective{}, exit(ExitRefused, bsname.Cmd+" goal "+req.Name+" discard (the keeper)", nil, "%s: it has ended; only a discard changes an ended goal", what)
		case e.Discarded:
			return logbook.Effective{}, exit(ExitRefused, "", nil, "%s: the keeper discarded it", what)
		}
	}
	change := logbook.GoalChange{By: req.By}
	switch {
	case discard:
		change.Discard = true
	case req.Waive != "":
		if req.Waive == goal.NoBackground {
			return logbook.Effective{}, exit(ExitRefused, bsname.Cmd+" done --force, or "+bsname.Cmd+" end --force", nil, "--waive %s: the gate is waived per call with --force, never for good", req.Waive)
		}
		var runs []string
		for _, n := range slices.Concat(e.Handoff(), e.Complete(), e.Prune()) {
			if n != goal.NoBackground && !slices.Contains(runs, n) {
				runs = append(runs, n)
			}
		}
		if !slices.Contains(runs, req.Waive) {
			return logbook.Effective{}, exit(ExitRefused, "", nil, "--waive %s: --goal %s runs no such check (it runs %s)", req.Waive, e.Goal, strings.Join(runs, ", "))
		}
		change.Waive = req.Waive
	default:
		g, err := goal.Parse(req.Values)
		if err != nil {
			return logbook.Effective{}, exit(ExitRefused, "", err, "%s", what)
		}
		for i, p := range g.Parts {
			g.Parts[i].Fragment = prompt.Hash(p.Type)
		}
		if _, err := c.resolveReview(ctx, &g); err != nil {
			return logbook.Effective{}, err
		}
		// A push goal's workspace starts at its target's head: one made for another goal
		// can't be re-cut there, so an amend never adds a push target.
		for _, p := range g.Parts {
			if p.Type == "push" && !slices.ContainsFunc(e.Parts, func(o goal.Part) bool { return o.Type == "push" && o.Target == p.Target }) {
				return logbook.Effective{}, exit(ExitRefused, bsname.Cmd+" end --incomplete "+req.Name+", then "+bsname.Cmd+" spawn --goal "+p.String()+" "+sp.Bead, nil,
					"--goal %s: a push goal's workspace starts at its target's head, and %s's was made for --goal %s", p, req.Name, e.Goal)
			}
		}
		if err := c.refuse(g, sp.Workspace); err != nil {
			return logbook.Effective{}, err
		}
		// Only a spawn from before bs froze its bead has none: nothing to tell a change from.
		if sp.At == nil && slices.ContainsFunc(g.Parts, goal.Part.ReadsBead) {
			return logbook.Effective{}, exit(ExitRefused, bsname.Cmd+" goal "+sp.Name+" discard (the keeper), then "+bsname.Cmd+" end "+sp.Name+" and "+bsname.Cmd+" spawn --goal "+g.String()+" "+sp.Bead, nil,
				"%s --goal %s: it was spawned before bs froze its bead, so there is nothing to tell a change from", what, g)
		}
		change.Goal = &g
	}
	if _, err := c.Logbook.RecordGoal(ctx, sp.ID, sp.Name, change); err != nil {
		return logbook.Effective{}, err
	}
	e, err = c.Logbook.Goal(ctx, sp)
	if err != nil || sp.Ended {
		return e, err
	}
	body := fmt.Sprintf("%s changed your goal: it is now --goal %s. Hand it off with %s done when it holds.\n", req.By, e.Goal, bsname.Cmd)
	for _, p := range e.Parts {
		if p.Type == "review" {
			body += fmt.Sprintf("Your review comment on %s must carry the marker bs:%s: %s done looks for it.\n", p.Target, sp.ID, bsname.Cmd)
		}
	}
	body += prompt.Goal(prompt.Worker, e.Types()) // the fragments its hash records
	if slices.ContainsFunc(e.Parts, goal.Part.Reviewed) {
		body += prompt.Host(prompt.Worker, c.Review.Name())
	}
	switch {
	case change.Discard:
		body = req.By + " discarded your goal: stop, and hand nothing off"
	case change.Waive != "":
		body = fmt.Sprintf("%s waived the %s check of your goal (--goal %s)", req.By, change.Waive, e.Goal)
	}
	_, err = c.Logbook.Send(ctx, req.By, sp.Name, body) // read only by this spawn: mail is scoped to the open spawn
	return e, err
}

// recordedAbove names each session this logbook recorded (its queen, an open spawn's
// start) among self and its ancestors.
func (c *Core) recordedAbove(ctx context.Context, self int) ([]string, error) {
	up, err := c.ancestors(self)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, pid := range up {
		who, err := c.recordedAs(ctx, pid)
		if err != nil {
			return nil, err
		}
		if who != "" && !slices.Contains(out, who) {
			out = append(out, who)
		}
	}
	return out, nil
}
