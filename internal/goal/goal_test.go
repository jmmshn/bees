package goal_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/goal"
)

func TestParseResolvesEachBuiltInType(t *testing.T) {
	for v, want := range map[string]goal.Goal{
		"pr":                   {Type: "pr", Complete: []string{goal.PRMerged}},
		"pr:ready":             {Type: "pr", State: "ready", AtHandoff: true},
		"pr:draft":             {Type: "pr", State: "draft", AtHandoff: true},
		"pr=https://x/1":       {Type: "pr", Target: "https://x/1", Complete: []string{goal.PRLanded}},
		"pr:ready=https://x/1": {Type: "pr", State: "ready", Target: "https://x/1", AtHandoff: true},
		"push=https://x/1":     {Type: "push", Target: "https://x/1", Complete: []string{goal.PRMerged}},
		"push:draft=u=v":       {Type: "push", State: "draft", Target: "u=v", AtHandoff: true}, // the target is everything after the first =
		"review=https://x2":    {Type: "review", Target: "https://x2", AtHandoff: true},
		"verdict":              {Type: "verdict", AtHandoff: true},
		"note":                 {Type: "note", AtHandoff: true},
		"beads":                {Type: "beads", AtHandoff: true},
		"branch":               {Type: "branch", Complete: []string{goal.BranchMerged}},
		"branch:ready":         {Type: "branch", State: "ready", AtHandoff: true},
	} {
		p, err := goal.Parse(v)
		if err != nil {
			t.Fatalf("Parse(%q): %v", v, err)
		}
		if p.Type != want.Type || p.State != want.State || p.Target != want.Target || p.AtHandoff != want.AtHandoff ||
			!want.AtHandoff && !slices.Equal(p.Complete, want.Complete) || len(p.Handoff) == 0 {
			t.Errorf("Parse(%q) = %+v, want %+v", v, p, want)
		}
		if p.String() != v {
			t.Errorf("%q reads back as %q", v, p.String())
		}
	}
}

// A bad value says what is wrong with it, and no goal is ErrNoGoal.
func TestParseRefusesABadValue(t *testing.T) {
	if _, err := goal.Parse(""); !errors.Is(err, goal.ErrNoGoal) {
		t.Fatalf("no goal: %v", err)
	}
	for _, v := range []string{"prr", "pr:merged", "push", "review=", "verdict=x", "pr=", "legacy", "discard", "mixed", "push=-x", "custom"} {
		if _, err := goal.Parse(v); !errors.Is(err, goal.ErrBad) || !strings.Contains(err.Error(), "\""+v+"\"") {
			t.Errorf("Parse(%q) = %v, want ErrBad naming it", v, err)
		}
	}
}

// pr=<url> stacks: its handoff first checks the target is still there to land on, and it
// completes and prunes on pr_landed, never on a merge into the target (Amendment 4).
func TestAStackedPRLandsOnlyOnTheProjectBase(t *testing.T) {
	g, err := goal.Parse("pr=https://x/1")
	if err != nil {
		t.Fatal(err)
	}
	if g.Handoff[0] != goal.PRTargetOpen || !slices.Equal(g.Prune, []string{goal.PRLanded}) || slices.Contains(g.Complete, goal.PRMerged) {
		t.Fatalf("pr= = %+v", g)
	}
	if d, _ := goal.Parse("pr:draft=https://x/1"); !slices.Equal(d.Prune, []string{goal.PRClosed}) {
		t.Fatalf("pr:draft= prunes on %v, want %s: a draft may close unmerged", d.Prune, goal.PRClosed)
	}
}

// A goal recorded before one goal per spawn decodes as a read-only mixed goal: its parts'
// checks once each, complete at handoff only when every part was, and it records back
// as it was.
func TestAnOldMixedGoalUnitesItsParts(t *testing.T) {
	push, _ := goal.Parse("push=https://x/1")
	pr, _ := goal.Parse("pr")
	pr.Repo = "o/r"
	rec := []byte(`{"parts":[` + partOf(t, push) + `,` + partOf(t, pr) + `]}`)
	var g goal.Goal
	if err := json.Unmarshal(rec, &g); err != nil {
		t.Fatal(err)
	}
	if g.Type != goal.MixedType || g.String() != "push=https://x/1 pr" || g.AtHandoff || g.Repo != "o/r" || !g.Reviewed() {
		t.Fatalf("mixed = %+v", g)
	}
	if !slices.Contains(g.Handoff, goal.PRHead) || !slices.Contains(g.Handoff, goal.PRNotDraft) {
		t.Fatalf("handoff = %v", g.Handoff)
	}
	if n := len(slices.Compact(slices.Sorted(slices.Values(g.Handoff)))); n != len(g.Handoff) {
		t.Fatalf("handoff repeats a check: %v", g.Handoff)
	}
	if !slices.Equal(g.Complete, []string{goal.PRMerged}) || !slices.Equal(g.Types(), []string{"push", "pr"}) {
		t.Fatalf("complete = %v, types = %v", g.Complete, g.Types())
	}
	if b, err := json.Marshal(g); err != nil || string(b) != string(rec) {
		t.Fatalf("re-recorded as\n%s (%v)\nwant\n%s", b, err, rec)
	}
}

// partOf is g's one recorded part.
func partOf(t *testing.T, g goal.Goal) string {
	t.Helper()
	b, err := json.Marshal(g)
	var rec struct{ Parts []json.RawMessage }
	if err != nil || json.Unmarshal(b, &rec) != nil || len(rec.Parts) != 1 {
		t.Fatalf("%s: %s, %v", g, b, err)
	}
	return string(rec.Parts[0])
}

// The frozen goal survives the logbook's JSON unchanged (ADR-0010: frozen), as one part.
func TestAGoalRoundTripsThroughJSON(t *testing.T) {
	g, _ := goal.Parse("pr:draft")
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), `{"parts":[{"type":"pr","state":"draft",`) {
		t.Fatalf("recorded as %s", b)
	}
	var back goal.Goal
	if err := json.Unmarshal(b, &back); err != nil || back.String() != g.String() || !slices.Equal(back.Handoff, g.Handoff) {
		t.Fatalf("round trip: %+v, %v", back, err)
	}
	if !goal.Legacy().IsLegacy() || g.IsLegacy() {
		t.Fatal("IsLegacy")
	}
}
