package cli

import "testing"

func TestNaturalCmp(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"bees-1-2", "bees-1-10", -1},
		{"bees-1-13", "bees-1-2", 1},
		{"bees-13-78-17", "bees-13-78-17", 0},
		{"lt-1-9", "lt-1-10", -1},
		{"toy-1", "toy-1-1", -1},
		{"queen-bees", "bees-1", 1},
		{"a07", "a7", 0},
	} {
		if got := naturalCmp(c.a, c.b); got != c.want {
			t.Errorf("naturalCmp(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
