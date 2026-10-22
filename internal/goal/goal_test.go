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
	for v, want := range map[string]goal.Part{
		"pr":                {Type: "pr", Complete: []string{goal.PRMerged}},
		"pr:ready":          {Type: "pr", State: "ready", AtHandoff: true},
		"pr:draft":          {Type: "pr", State: "draft", AtHandoff: true},
		"push=https://x/1":  {Type: "push", Target: "https://x/1", Complete: []string{goal.PRMerged}},
		"push:draft=u=v":    {Type: "push", State: "draft", Target: "u=v", AtHandoff: true}, // the target is everything after the first =
		"review=https://x2": {Type: "review", Target: "https://x2", AtHandoff: true},
		"verdict":           {Type: "verdict", AtHandoff: true},
		"note":              {Type: "note", AtHandoff: true},
		"beads":             {Type: "beads", AtHandoff: true},
		"branch":            {Type: "branch", Complete: []string{goal.BranchMerged}},
		"branch:ready":      {Type: "branch", State: "ready", AtHandoff: true},
	} {
		g, err := goal.Parse([]string{v})
		if err != nil {
			t.Fatalf("Parse(%q): %v", v, err)
		}
		p := g.Parts[0]
		if p.Type != want.Type || p.State != want.State || p.Target != want.Target || p.AtHandoff != want.AtHandoff ||
			!want.AtHandoff && !slices.Equal(p.Complete, want.Complete) || len(p.Handoff) == 0 {
			t.Errorf("Parse(%q) = %+v, want %+v", v, p, want)
		}
		if p.String() != v {
			t.Errorf("%q reads back as %q", v, p.String())
		}
	}
}

// Rule G: every bad value is named at once, and no goal is ErrNoGoal.
func TestParseRefusesEveryBadValueAtOnce(t *testing.T) {
	if _, err := goal.Parse(nil); !errors.Is(err, goal.ErrNoGoal) {
		t.Fatalf("no goal: %v", err)
	}
	bad := []string{"prr", "pr:merged", "push", "review=", "verdict=x", "pr=x", "legacy", "discard", "push=-x", "custom"}
	_, err := goal.Parse(append(bad, "pr"))
	if !errors.Is(err, goal.ErrBad) {
		t.Fatalf("err = %v, want ErrBad", err)
	}
	for _, v := range bad {
		if !strings.Contains(err.Error(), "\""+v+"\"") {
			t.Errorf("error doesn't name %q:\n%v", v, err)
		}
	}
	if _, err := goal.Parse([]string{"pr", "pr"}); !errors.Is(err, goal.ErrBad) {
		t.Errorf("a part given twice: %v", err)
	}
}

// A mixed goal runs every part's checks once each, the gate first.
func TestAMixedGoalUnitesItsParts(t *testing.T) {
	g, err := goal.Parse([]string{"push=https://x/1", "pr"})
	if err != nil {
		t.Fatal(err)
	}
	h := g.Handoff()
	if h[0] != goal.NoBackground || !slices.Contains(h, goal.PushMoved) || !slices.Contains(h, goal.PROpen) {
		t.Fatalf("handoff = %v", h)
	}
	if n := len(slices.Compact(slices.Sorted(slices.Values(h)))); n != len(h) {
		t.Fatalf("handoff repeats a check: %v", h)
	}
	if c := g.Complete(); !slices.Equal(c, []string{goal.PRMerged}) {
		t.Fatalf("complete = %v", c)
	}
}

// The frozen goal survives the logbook's JSON unchanged (ADR-0010: frozen).
func TestAGoalRoundTripsThroughJSON(t *testing.T) {
	g, _ := goal.Parse([]string{"pr:draft", "verdict"})
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	var back goal.Goal
	if err := json.Unmarshal(b, &back); err != nil || back.String() != g.String() || !slices.Equal(back.Handoff(), g.Handoff()) {
		t.Fatalf("round trip: %+v, %v", back, err)
	}
	if !goal.Legacy().IsLegacy() || g.IsLegacy() {
		t.Fatal("IsLegacy")
	}
}
