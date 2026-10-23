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
// nothing (bs check): the queen runs it before merging, a worker on itself. It returns
// every result so far; the error is exit 6 at the first check that couldn't run, else exit
// 7 when one failed.
func (c *Core) Check(ctx context.Context, w Who, name string, self int) ([]logbook.CheckResult, error) {
	if err := c.permit(w, "check", name); err != nil {
		return nil, err
	}
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
	Who    Who // who records it
	Name   string
	Values []string // the new --goal values; ["discard"] is a discard
	Waive  string
}

// Goal amends worker name's live, incomplete goal, waives one of its checks, or discards
// it (ADR-0010). The queen or the keeper may amend or waive, never a worker; only the
// keeper may discard, at any time. An amend re-runs the spawn-time refusals for the new goal, and the
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
	act := "goal"
	if discard {
		act = "discard"
	}
	if err := c.permit(req.Who, act, ""); err != nil {
		return logbook.Effective{}, err
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
	change := logbook.GoalChange{By: req.Who.Name}
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
	body := fmt.Sprintf("%s changed your goal: it is now --goal %s. Hand it off with %s done when it holds.\n", req.Who.Name, e.Goal, bsname.Cmd)
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
		body = req.Who.Name + " discarded your goal: stop, and hand nothing off"
	case change.Waive != "":
		body = fmt.Sprintf("%s waived the %s check of your goal (--goal %s)", req.Who.Name, change.Waive, e.Goal)
	}
	_, err = c.Logbook.Send(ctx, req.Who.Name, sp.Name, body) // read only by this spawn: mail is scoped to the open spawn
	return e, err
}

// can is the permission table (ADR-0010 Identity): what each role may do, beside sending
// as itself and reading its own inbox, which every session may. A worker's done and check
// are of itself only.
var can = map[string][]string{
	Keeper:   {"spawn", "end", "prune", "goal", "discard", "check"},
	"queen":  {"spawn", "end", "prune", "goal", "check"},
	"worker": {"done", "check"},
}

// doing is how a refusal names each act.
var doing = map[string]string{"spawn": "spawns a worker", "end": "ends a worker", "prune": "prunes", "goal": "changes a goal",
	"discard": "discards a goal", "check": "checks another worker", "done": "hands off"}

// permit refuses w doing act unless its role may; name is the worker check runs on.
func (c *Core) permit(w Who, act, name string) error {
	role := w.Role()
	if slices.Contains(can[role], act) && (role != "worker" || act != "check" || name == w.Name) {
		return nil
	}
	var who []string
	for _, r := range []string{"queen", Keeper, "worker"} {
		if slices.Contains(can[r], act) && (r != "worker" || act != "check") {
			who = append(who, as(r))
		}
	}
	next := bsname.Cmd + " send " + Queen(c.Project) + " \"...\""
	switch role {
	case "queen":
		next = "ask the keeper"
	case Keeper:
		next = bsname.Cmd + " status"
	}
	return exit(ExitRefused, next, nil, "%s runs this as %s; only %s %s", w.Name, as(role), strings.Join(who, " or "), doing[act])
}

// as is a role with its article.
func as(role string) string {
	if role == "worker" {
		return "a worker"
	}
	return "the " + role
}
