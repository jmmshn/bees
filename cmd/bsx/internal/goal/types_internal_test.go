package goal

import (
	"maps"
	"slices"
	"testing"
)

// BuiltinTypes spells out the types map's keys in a fixed order: the two lists agree.
func TestBuiltinTypesAreTheTypes(t *testing.T) {
	got, want := slices.Sorted(slices.Values(BuiltinTypes())), slices.Sorted(maps.Keys(types))
	if !slices.Equal(got, want) {
		t.Fatalf("BuiltinTypes() = %v; types has %v", got, want)
	}
}
