package goal

import (
	"slices"
	"testing"
)

// Every check a built-in goal runs is in builtins, so a custom goal may name it too.
func TestEveryCheckAGoalRunsIsBuiltIn(t *testing.T) {
	for typ, tt := range types {
		for _, st := range tt.states {
			p := resolve(typ, st, "")
			for _, c := range slices.Concat(p.Handoff, p.Complete, p.Prune) {
				if !IsCheck(c) {
					t.Errorf("%s:%s runs %s, which IsCheck doesn't know", typ, st, c)
				}
			}
		}
	}
	if !IsCheck(NoBackground) {
		t.Error("the gate's check is not built in")
	}
}
