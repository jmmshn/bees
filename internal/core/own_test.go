package core

import (
	"slices"
	"testing"

	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/port"
)

// cidHarness is a harness with no --name (agy): it owns a launch by --cid <name>.
type cidHarness struct{ port.Harness }

func (cidHarness) Kind() string { return "agy" }
func (cidHarness) Owns(argv []string, name string) bool {
	i := slices.Index(argv, "--cid")
	return i >= 0 && i+1 < len(argv) && argv[i+1] == name
}

// Which process is a session's own is its harness's rule, not claude's --name.
func TestIsOwnAsksTheHarness(t *testing.T) {
	h := cidHarness{}
	for _, c := range []struct {
		f    herdr.PaneProcess
		want bool
	}{
		{herdr.PaneProcess{Name: "agy", Argv: []string{"agy", "--cid", "bees-8"}}, true},
		{herdr.PaneProcess{Name: "agy", Argv: []string{"agy", "--name", "bees-8"}}, false},
		{herdr.PaneProcess{Name: "claude", Argv: []string{"claude", "--cid", "bees-8"}}, false},
	} {
		if got := isOwn(c.f, h, "bees-8"); got != c.want {
			t.Errorf("isOwn(%v) = %v, want %v", c.f, got, c.want)
		}
	}
}
