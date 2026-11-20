package proc

import (
	"slices"
	"testing"
)

// lsof's -F pn output, as macOS prints it for -d cwd: a pid line, then its cwd's name.
func TestLsofCwdsPicksThePidsWorkingInsideDir(t *testing.T) {
	out := "p1\nn/\np201\nn/w/toy-1\np202\nn/w/toy-1/sub\np203\nn/w/toy-10\np204\nn/w/toy-1/../x\np\nn/w/toy-1\np205\nnrelative\n"
	if got, want := lsofCwds(out, "/w/toy-1"), []int{201, 202}; !slices.Equal(got, want) {
		t.Fatalf("lsofCwds = %v, want %v", got, want)
	}
	if got := lsofCwds("", "/w/toy-1"); len(got) != 0 {
		t.Fatalf("lsofCwds(empty) = %v", got)
	}
}
