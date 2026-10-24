package core

import (
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
)

// The brief names where the worker works, from the spawn record: a harness whose cwd
// isn't the worktree (agy) learns it only there (bees-13.98).
func TestBriefNamesTheWorkspace(t *testing.T) {
	for _, c := range []struct {
		ws   logbook.Workspace
		want string
	}{
		{logbook.Workspace{Kind: config.GitWorktree, Dir: "/r/toy-WT/toy-1", Branch: "toy-1"}, "Your worktree: /r/toy-WT/toy-1, branch toy-1.\n"},
		{logbook.Workspace{Kind: config.CommandKind, Dir: "/r/toy-WS/toy-1", Branch: "toy-1"}, "Your workspace: /r/toy-WS/toy-1, branch toy-1.\n"},
		{logbook.Workspace{Kind: config.CommandKind, Dir: "/r/toy-WS/toy-1"}, "Your workspace: /r/toy-WS/toy-1.\n"},
	} {
		got := brief(port.Bead{ID: "toy-1", Title: "t"}, goal.Goal{}, c.ws, nil, "", nil)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s brief = %q, want %q", c.ws.Kind, got, c.want)
		}
	}
}
