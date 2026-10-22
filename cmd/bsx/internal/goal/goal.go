// Package goal is what a worker owes the queen, fixed at spawn (ADR-0010): the grammar of
// --goal, and the frozen form a spawned entry holds, every check named, so bs done, bs end
// and prune read the record and never config.
package goal

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrNoGoal is a spawn with no --goal: the queen always names the goal (Rule D).
var ErrNoGoal = errors.New("--goal is required: name what the worker hands off (pr, pr:ready, pr:draft, push=<pr-url>, review=<pr-url>, verdict, note, beads, branch)")

// ErrBad is a --goal value bs can't resolve.
var ErrBad = errors.New("bad --goal")

// Goal is a resolved goal: one part per --goal, and every part must hold.
type Goal struct {
	Parts []Part `json:"parts"`
}

// Part is one resolved --goal value: its type, state and target as given, and the checks
// each stage runs, by name. A check name is a built-in check (Builtin); custom types and
// their cmd: checks arrive with bees-13.11.
type Part struct {
	Type   string `json:"type"`
	State  string `json:"state,omitempty"`
	Target string `json:"target,omitempty"`
	// Handoff runs at bs done. The no_background gate is implicit in every handoff and
	// isn't listed.
	Handoff []string `json:"handoff"`
	// AtHandoff: the part is complete once its handoff passed. Otherwise Complete must
	// pass, and with no Complete checks (legacy) the part is complete as it stands.
	AtHandoff bool     `json:"at_handoff,omitempty"`
	Complete  []string `json:"complete"`
	// Prune must pass before bs prune removes the workspace; none means at once.
	Prune []string `json:"prune"`
}

// String is the part as --goal spells it.
func (p Part) String() string {
	s := p.Type
	if p.State != "" {
		s += ":" + p.State
	}
	if p.Target != "" {
		s += "=" + p.Target
	}
	return s
}

// String is the goal as --goal values, space separated.
func (g Goal) String() string {
	var s []string
	for _, p := range g.Parts {
		s = append(s, p.String())
	}
	return strings.Join(s, " ")
}

// Legacy is the goal a spawn recorded before goals reads as.
func Legacy() Goal { return Goal{Parts: []Part{resolve("legacy", "", "")}} }

// IsLegacy reports whether g is Legacy.
func (g Goal) IsLegacy() bool { return len(g.Parts) == 1 && g.Parts[0].Type == "legacy" }

// The built-in checks. Each names one fact bs finds (Rule D).
const (
	NoBackground      = "no_background"       // no process under the harness its start didn't see
	PRExists          = "pr_exists"           // each handed-off PR exists in the spawn's repo
	PROpen            = "pr_open"             // each is open
	PRNotDraft        = "pr_not_draft"        // each is not a draft
	PRWorkspaceCommit = "pr_workspace_commit" // each one's range holds a workspace commit
	PRPushed          = "pr_pushed"           // the PR's head is the pushed workspace commit
	PRCurrent         = "pr_current"          // current with its own base
	CIGreen           = "ci_green"            // gh pr checks all pass
	PRMerged          = "pr_merged"           // every handed-off PR merged
	PRClosed          = "pr_merged_or_closed" // every handed-off PR merged or closed
	PushMoved         = "push_moved"          // the target's head descends from a workspace commit
	ReviewComment     = "review_comment"      // a comment on the target since spawn carries bs:<spawn id>
	VerdictGiven      = "verdict_given"       // --answer and --report present
	NoteChanged       = "note_changed"        // the bead's notes or design differ from spawn's hash
	BeadCreated       = "bead_created"        // a bead created under the bead since spawn
	BranchAhead       = "branch_ahead"        // the recorded branch is ahead of its base
	Clean             = "clean"               // the workspace has no uncommitted or untracked files
	BranchMerged      = "branch_merged"       // the branch is merged into its base
)

// prHandoff is what bs checks of each handed-off PR for pr and pr:ready.
var prHandoff = []string{PROpen, PRNotDraft, PRWorkspaceCommit, PRPushed, PRCurrent, CIGreen}

// types are the built-in goal types: each state, and whether it takes a target.
var types = map[string]struct {
	states []string // "" is the bare type
	target bool
}{
	"pr":      {states: []string{"", "ready", "draft"}},
	"push":    {states: []string{"", "ready", "draft"}, target: true},
	"review":  {states: []string{""}, target: true},
	"verdict": {states: []string{""}},
	"note":    {states: []string{""}},
	"beads":   {states: []string{""}},
	"branch":  {states: []string{"", "ready"}},
}

// resolve is the frozen part of a valid type, state and target (ADR-0010's goal table).
func resolve(typ, state, target string) Part {
	p := Part{Type: typ, State: state, Target: target}
	switch typ + ":" + state {
	case "pr:":
		p.Handoff, p.Complete, p.Prune = prHandoff, []string{PRMerged}, []string{PRMerged}
	case "pr:ready":
		p.Handoff, p.AtHandoff, p.Prune = prHandoff, true, []string{PRMerged}
	case "pr:draft":
		p.Handoff, p.AtHandoff, p.Prune = []string{PRExists, PRWorkspaceCommit, PRPushed}, true, []string{PRClosed}
	case "push:":
		p.Handoff, p.Complete, p.Prune = []string{PushMoved, PRCurrent, CIGreen}, []string{PRMerged}, []string{PRMerged}
	case "push:ready":
		p.Handoff, p.AtHandoff, p.Prune = []string{PushMoved, PRCurrent, CIGreen}, true, []string{PRMerged}
	case "push:draft":
		p.Handoff, p.AtHandoff, p.Prune = []string{PushMoved}, true, []string{PRClosed}
	case "review:":
		p.Handoff, p.AtHandoff = []string{ReviewComment}, true
	case "verdict:":
		p.Handoff, p.AtHandoff = []string{VerdictGiven}, true
	case "note:":
		p.Handoff, p.AtHandoff = []string{NoteChanged}, true
	case "beads:":
		p.Handoff, p.AtHandoff = []string{BeadCreated}, true
	case "branch:":
		p.Handoff, p.Complete, p.Prune = []string{BranchAhead, Clean}, []string{BranchMerged}, []string{BranchMerged}
	case "branch:ready":
		p.Handoff, p.AtHandoff, p.Prune = []string{BranchAhead, Clean}, true, []string{BranchMerged}
	case "legacy:":
		p.Prune = []string{BranchMerged}
	}
	p.Handoff = slices.Clip(p.Handoff)
	if p.Handoff == nil {
		p.Handoff = []string{}
	}
	if p.Complete == nil {
		p.Complete = []string{}
	}
	if p.Prune == nil {
		p.Prune = []string{}
	}
	return p
}

// Parse resolves the --goal values of one spawn, each <type>[:<state>][=<target>] with the
// target everything after the first =. Every value is checked and every problem reported
// at once (Rule G); none is ErrNoGoal.
func Parse(values []string) (Goal, error) {
	if len(values) == 0 {
		return Goal{}, ErrNoGoal
	}
	var g Goal
	var errs []error
	seen := map[string]bool{}
	for _, v := range values {
		head, target, hasTarget := strings.Cut(v, "=")
		typ, state, _ := strings.Cut(head, ":")
		t, ok := types[typ]
		switch {
		case typ == "legacy", typ == "discard":
			errs = append(errs, fmt.Errorf("%w %q: %s is never given at spawn", ErrBad, v, typ))
		case !ok:
			errs = append(errs, fmt.Errorf("%w %q: no goal type %q (built in: pr, push, review, verdict, note, beads, branch; custom types arrive with bees-13.11)", ErrBad, v, typ))
		case !slices.Contains(t.states, state):
			errs = append(errs, fmt.Errorf("%w %q: %s has no state %q", ErrBad, v, typ, state))
		case t.target && (!hasTarget || target == ""):
			errs = append(errs, fmt.Errorf("%w %q: %s needs a target: %s=<pr-url>", ErrBad, v, typ, head))
		case !t.target && hasTarget:
			errs = append(errs, fmt.Errorf("%w %q: %s takes no target", ErrBad, v, typ))
		case strings.HasPrefix(target, "-"):
			errs = append(errs, fmt.Errorf("%w %q: a target can't start with -", ErrBad, v))
		case seen[typ+"="+target]:
			errs = append(errs, fmt.Errorf("%w %q: given twice", ErrBad, v))
		default:
			seen[typ+"="+target] = true
			g.Parts = append(g.Parts, resolve(typ, state, target))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Goal{}, err
	}
	return g, nil
}

// Handoff is every check g's handoff runs, in order, each once, the implicit
// no_background gate first.
func (g Goal) Handoff() []string {
	return g.union(func(p Part) []string { return p.Handoff }, NoBackground)
}

// Complete is every completion check of the parts not complete at handoff.
func (g Goal) Complete() []string {
	return g.union(func(p Part) []string {
		if p.AtHandoff {
			return nil
		}
		return p.Complete
	})
}

// Prune is every prune check of g.
func (g Goal) Prune() []string { return g.union(func(p Part) []string { return p.Prune }) }

func (g Goal) union(of func(Part) []string, first ...string) []string {
	out := slices.Clone(first)
	for _, p := range g.Parts {
		for _, c := range of(p) {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out
}
