package proc

import (
	"slices"
	"testing"
)

// Descendants come level by level, so a grandchild with a lower pid (pid wraparound) never
// comes before a child: harness lookups take the nearest match.
func TestDescendantsComeLevelByLevel(t *testing.T) {
	all := []Process{{PID: 5}, {PID: 9}, {PID: 3}, {PID: 7}}
	parent := map[int]int{9: 1, 3: 9, 7: 1, 5: 2}
	var got []int
	for _, p := range descendants(1, all, parent) {
		got = append(got, p.PID)
	}
	if !slices.Equal(got, []int{9, 7, 3}) {
		t.Fatalf("descendants(1) = %v, want children 9 and 7, then grandchild 3", got)
	}
}
