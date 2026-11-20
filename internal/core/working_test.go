package core

import (
	"slices"
	"testing"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/proc"
)

// procTable is a process table: each pid's parent, and its environment (nil: unreadable, as
// on macOS). Every pid works in the dir InDir is asked about.
type procTable struct {
	port.Proc
	parent map[int]int
	env    map[int][]string
}

func (t procTable) InDir(string) ([]proc.Process, error) {
	var out []proc.Process
	for pid := range t.parent {
		out = append(out, proc.Process{PID: pid, Start: "1"})
	}
	slices.SortFunc(out, func(a, b proc.Process) int { return a.PID - b.PID })
	return out, nil
}
func (t procTable) Parent(pid int) (int, error) { return t.parent[pid], nil }
func (t procTable) Command(int) (string, error) { return "sleep 30", nil }
func (t procTable) Environ(pid int) ([]string, error) {
	if t.env[pid] == nil {
		return nil, proc.ErrNoEnviron
	}
	return t.env[pid], nil
}

// working leaves out bs and its ancestors (a bs run from inside the worktree), ties to
// the worker only what its pane's environment names, and never looks in a shared dir.
func TestWorkingSkipsSelfAndTiesOnlyItsPane(t *testing.T) {
	// 10 → 11 → 12 (bs, self); 20 from the worker's pane; 30 from another; 40 unreadable.
	c := &Core{Proc: procTable{
		parent: map[int]int{10: 1, 11: 10, 12: 11, 20: 1, 30: 1, 40: 1},
		env: map[int][]string{
			20: {"HERDR_PANE_ID=w2:p1"}, 30: {"HERDR_PANE_ID=w3:p1"}, 10: {}, 11: {}, 12: {},
		},
	}}
	sp := logbook.Spawn{Workspace: logbook.Workspace{Kind: config.GitWorktree, Dir: "/w/toy-1"}, Herdr: logbook.Pane{Pane: "w2:p1"}}
	in, err := c.working(sp, 12, []int{40})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, w := range in {
		got = append(got, w.String())
	}
	want := []string{"pid 20     sleep 30", "pid 30     sleep 30   (not started from its pane)"}
	if !slices.Equal(got, want) {
		t.Fatalf("working = %q, want %q", got, want)
	}
	if in, _ := c.working(sp, 99, nil); len(in) != 6 || in[5].Mine { // 40: no environment to read, never its own
		t.Fatalf("working(self 99) = %v", in)
	}
	sp.Workspace.Kind = "none"
	if in, err := c.working(sp, 12, nil); err != nil || in != nil {
		t.Fatalf("working(a shared dir) = %v, %v; want nothing looked at", in, err)
	}
}
