package guards_test

import (
	"testing"

	"github.com/jmmshn/bees/internal/guards"
)

// Decide denies a shell command a deny glob matches and gives everything else, a tool
// with no command line included, no decision; it never allows (bees-13.13.9).
func TestDecide(t *testing.T) {
	w := guards.Worker{Deny: []string{"git push *main*"}}
	for _, c := range []struct {
		call guards.Call
		want guards.Verdict
	}{
		{guards.Call{Tool: "run_command", Command: "git push origin main"}, guards.Deny},
		{guards.Call{Tool: "run_command", Command: "git push origin feature"}, guards.None},
		{guards.Call{Tool: "view_file"}, guards.None},
	} {
		d := guards.Decide(c.call, w)
		if d.Verdict != c.want || (d.Verdict == guards.Deny) != (d.Reason != "") {
			t.Errorf("Decide(%+v) = %+v, want verdict %d", c.call, d, c.want)
		}
	}
	if d := guards.Decide(guards.Call{Tool: "view_file"}, guards.Worker{Deny: []string{"*"}}); d.Verdict != guards.None {
		t.Errorf("a * glob judged a tool with no command line: %+v", d)
	}
}
