// Package goal is what a worker owes the queen, fixed at spawn (ADR-0010): the grammar of
// --goal, and the frozen form a spawned entry holds, every check named, so bs done, bs end
// and prune read the record and never config.
package goal

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrNoGoal is a spawn with no --goal: the queen always names the goal (Rule D).
var ErrNoGoal = errors.New("--goal is required: name what the worker hands off (pr, pr:ready, pr:draft, push=<pr-url>, review=<pr-url>, verdict, note, beads, branch)")

// ErrBad is a --goal value bs can't resolve.
var ErrBad = errors.New("bad --goal")

// Goal is a resolved goal: one --goal value, its type, state and target as given, and the
// checks each stage runs, by name (ADR-0010 Amendment 4: one goal per spawn). A check
// name is a built-in check (Builtin); custom types and their cmd: checks arrive with
// bees-13.11. Its JSON stays {"parts":[…]}, so every entry ever written decodes.
type Goal struct {
	Type, State, Target string
	// Handoff runs at bs done. The no_background gate is implicit in every handoff and
	// isn't listed.
	Handoff []string
	// AtHandoff: the goal is complete once its handoff passed. Otherwise Complete must
	// pass, and with no Complete checks (legacy) the goal is complete as it stands.
	AtHandoff bool
	Complete  []string
	// Prune must pass before bs prune removes the workspace; none means at once.
	Prune []string
	// Fragment is the sha256 of its type's fragment file at spawn (both roles' parts, before
	// {{…}} is filled: prompt.Hash); "" when its type has none yet.
	Fragment string
	// Repo is the review host's repo a pr, push or review goal's changes must belong to:
	// the one the project's checkout belongs to, found and frozen at spawn (Rule D).
	Repo string
	// Mixed is a goal recorded before one goal per spawn, as --goal spelled its parts
	// (Type MixedType): read-only, it can only be discarded, ended and pruned.
	Mixed []string
	parts []part // a mixed goal's parts, as recorded
}

// part is one --goal value as the logbook records it, {"parts":[part…]}.
type part struct {
	Type      string   `json:"type"`
	State     string   `json:"state,omitempty"`
	Target    string   `json:"target,omitempty"`
	Handoff   []string `json:"handoff"`
	AtHandoff bool     `json:"at_handoff,omitempty"`
	Complete  []string `json:"complete"`
	Prune     []string `json:"prune"`
	Fragment  string   `json:"fragment,omitempty"`
	Repo      string   `json:"repo,omitempty"`
}

// MarshalJSON is g as the logbook records it: {"parts":[g]}.
func (g Goal) MarshalJSON() ([]byte, error) {
	ps := g.parts
	if g.Mixed == nil {
		ps = []part{{g.Type, g.State, g.Target, g.Handoff, g.AtHandoff, g.Complete, g.Prune, g.Fragment, g.Repo}}
	}
	return json.Marshal(struct {
		Parts []part `json:"parts"`
	}{ps})
}

// UnmarshalJSON reads a recorded goal. One recorded before one goal per spawn with more
// than one part is a mixed goal: its checks the union of its parts', complete at handoff
// only when every part was.
func (g *Goal) UnmarshalJSON(b []byte) error {
	var rec struct {
		Parts []part `json:"parts"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return err
	}
	switch ps := rec.Parts; len(ps) {
	case 0:
		*g = Goal{}
	case 1:
		p := ps[0]
		*g = Goal{Type: p.Type, State: p.State, Target: p.Target, Handoff: p.Handoff, AtHandoff: p.AtHandoff,
			Complete: p.Complete, Prune: p.Prune, Fragment: p.Fragment, Repo: p.Repo}
	default:
		*g = Goal{Type: MixedType, AtHandoff: true, Handoff: []string{}, Complete: []string{}, Prune: []string{}, parts: ps}
		for _, p := range ps {
			g.Mixed = append(g.Mixed, p.spelling())
			g.Handoff = union(g.Handoff, p.Handoff)
			if !p.AtHandoff {
				g.AtHandoff, g.Complete = false, union(g.Complete, p.Complete)
			}
			g.Prune = union(g.Prune, p.Prune)
			g.Repo = cmp.Or(g.Repo, p.Repo)
		}
	}
	return nil
}

func union(a, b []string) []string {
	for _, c := range b {
		if !slices.Contains(a, c) {
			a = append(a, c)
		}
	}
	return a
}

func (p part) spelling() string { return spell(p.Type, p.State, p.Target) }

func spell(typ, state, target string) string {
	s := typ
	if state != "" {
		s += ":" + state
	}
	if target != "" {
		s += "=" + target
	}
	return s
}

// Types is g's type, or a mixed goal's parts' types, in order, each once.
func (g Goal) Types() []string {
	if g.Mixed == nil {
		return []string{g.Type}
	}
	var out []string
	for _, p := range g.parts {
		out = union(out, []string{p.Type})
	}
	return out
}

// Reviewed reports whether g's checks read changes on the review host.
func (g Goal) Reviewed() bool {
	return slices.ContainsFunc(g.Types(), func(t string) bool { return t == "pr" || t == "push" || t == "review" })
}

// TakesReport reports whether g's handoff takes --answer and --report.
func (g Goal) TakesReport() bool { return g.Answers() != nil }

// Answers is the --answer values g's handoff takes; none for a goal that takes no answer.
func (g Goal) Answers() []string {
	switch g.Type {
	case "verdict":
		return []string{"yes", "no"}
	case "review":
		return []string{"approve", "changes"}
	}
	return nil
}

// ReadsBead reports whether g's checks read the bead as spawn froze it.
func (g Goal) ReadsBead() bool { return g.Type == "note" || g.Type == "beads" }

// String is g as --goal spells it; a mixed goal's parts, space separated.
func (g Goal) String() string {
	if g.Mixed != nil {
		return strings.Join(g.Mixed, " ")
	}
	return spell(g.Type, g.State, g.Target)
}

// The goal types never given at spawn.
const (
	LegacyType  = "legacy"  // what a spawn recorded before goals reads as
	DiscardType = "discard" // what a keeper's discard amends a goal to
	MixedType   = "mixed"   // a goal recorded before one goal per spawn, with several parts
)

// Legacy is the goal a spawn recorded before goals reads as.
func Legacy() Goal { return resolve(LegacyType, "", "") }

// IsLegacy reports whether g is Legacy.
func (g Goal) IsLegacy() bool { return g.Type == LegacyType }

// The built-in checks. Each names one fact bs finds (Rule D).
const (
	NoBackground      = "no_background"       // no process under the harness its start didn't see
	PRExists          = "pr_exists"           // bs finds the worker's change from its branch
	PROpen            = "pr_open"             // it is open
	PRNotDraft        = "pr_not_draft"        // it is not a draft
	PRHead            = "pr_head"             // its head is the branch tip, ahead of the branch's start
	PRWorkspaceCommit = "pr_workspace_commit" // before Amendment 4: runs as pr_head
	PRPushed          = "pr_pushed"           // before Amendment 4: runs as pr_head
	PRCurrent         = "pr_current"          // current with its own base
	CIGreen           = "ci_green"            // every required CI check passes
	PRMerged          = "pr_merged"           // the change merged
	PRClosed          = "pr_merged_or_closed" // the change merged or closed
	PushMoved         = "push_moved"          // the target's head descends from a workspace commit
	ReviewComment     = "review_comment"      // before bs posted the review: runs as verdict_given
	VerdictGiven      = "verdict_given"       // --answer one of the goal's answers, and a report not blank
	NoteChanged       = "note_changed"        // the bead's notes or design differ from spawn's hash
	BeadCreated       = "bead_created"        // a bead created under the bead since spawn
	BranchAhead       = "branch_ahead"        // the recorded branch is ahead of its base
	Clean             = "clean"               // the workspace has no uncommitted or untracked files
	BranchMerged      = "branch_merged"       // the branch is merged into its base
)

// builtins are the built-in checks, for IsCheck; TestEveryCheckAGoalRunsIsBuiltIn keeps
// it in step with the goal table.
var builtins = []string{NoBackground, PRExists, PROpen, PRNotDraft, PRHead, PRWorkspaceCommit, PRPushed, PRCurrent,
	CIGreen, PRMerged, PRClosed, PushMoved, ReviewComment, VerdictGiven, NoteChanged, BeadCreated, BranchAhead,
	Clean, BranchMerged}

// IsCheck reports whether name is a built-in check.
func IsCheck(name string) bool { return slices.Contains(builtins, name) }

// BuiltinTypes is every built-in goal type a queen may give at spawn, in a fixed order.
func BuiltinTypes() []string {
	return []string{"pr", "push", "review", "verdict", "note", "beads", "branch"}
}

// IsType reports whether name is a built-in goal type, or one never given at spawn.
func IsType(name string) bool {
	_, ok := types[name]
	return ok || name == LegacyType || name == DiscardType || name == MixedType
}

// prHandoff is what bs checks of the worker's change for pr and pr:ready.
var prHandoff = []string{PROpen, PRNotDraft, PRHead, PRCurrent, CIGreen}

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

// resolve is the frozen goal of a valid type, state and target (ADR-0010's goal table).
func resolve(typ, state, target string) Goal {
	p := Goal{Type: typ, State: state, Target: target}
	switch typ + ":" + state {
	case "pr:":
		p.Handoff, p.Complete, p.Prune = prHandoff, []string{PRMerged}, []string{PRMerged}
	case "pr:ready":
		p.Handoff, p.AtHandoff, p.Prune = prHandoff, true, []string{PRMerged}
	case "pr:draft":
		p.Handoff, p.AtHandoff, p.Prune = []string{PRExists, PRHead}, true, []string{PRClosed}
	case "push:":
		p.Handoff, p.Complete, p.Prune = []string{PushMoved, PRCurrent, CIGreen}, []string{PRMerged}, []string{PRMerged}
	case "push:ready":
		p.Handoff, p.AtHandoff, p.Prune = []string{PushMoved, PRCurrent, CIGreen}, true, []string{PRMerged}
	case "push:draft":
		p.Handoff, p.AtHandoff, p.Prune = []string{PushMoved}, true, []string{PRClosed}
	case "review:":
		p.Handoff, p.AtHandoff = []string{VerdictGiven, PROpen}, true
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
	case LegacyType + ":":
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

// Parse resolves a spawn's --goal value, <type>[:<state>][=<target>] with the target
// everything after the first =; "" is ErrNoGoal. The CLI refuses a second --goal.
func Parse(v string) (Goal, error) {
	head, target, hasTarget := strings.Cut(v, "=")
	typ, state, _ := strings.Cut(head, ":")
	t, ok := types[typ]
	switch {
	case v == "":
		return Goal{}, ErrNoGoal
	case typ == LegacyType, typ == DiscardType, typ == MixedType:
		return Goal{}, fmt.Errorf("%w %q: %s is never given at spawn", ErrBad, v, typ)
	case !ok:
		return Goal{}, fmt.Errorf("%w %q: no goal type %q (built in: pr, push, review, verdict, note, beads, branch; custom types arrive with bees-13.11)", ErrBad, v, typ)
	case !slices.Contains(t.states, state):
		return Goal{}, fmt.Errorf("%w %q: %s has no state %q", ErrBad, v, typ, state)
	case t.target && (!hasTarget || target == ""):
		return Goal{}, fmt.Errorf("%w %q: %s needs a target: %s=<pr-url>", ErrBad, v, typ, head)
	case !t.target && hasTarget:
		return Goal{}, fmt.Errorf("%w %q: %s takes no target", ErrBad, v, typ)
	case strings.HasPrefix(target, "-"):
		return Goal{}, fmt.Errorf("%w %q: a target can't start with -", ErrBad, v)
	}
	return resolve(typ, state, target), nil
}
