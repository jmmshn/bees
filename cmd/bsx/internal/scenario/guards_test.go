package scenario_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmmshn/bees/cmd/bsx/internal/guards"
)

// deny is the deny list worker name's harness was started with.
func (x *toy) deny(name string) []string {
	x.t.Helper()
	fs, _ := filepath.Glob(filepath.Join(x.ports.CacheDir, name+"-deny-*.json"))
	if len(fs) != 1 {
		x.t.Fatalf("deny files of %s: %v", name, fs)
	}
	b, err := os.ReadFile(fs[0])
	if err != nil {
		x.t.Fatal(err)
	}
	var d []string
	if err := json.Unmarshal(b, &d); err != nil {
		x.t.Fatal(err)
	}
	return d
}

// Round 11's static guards: a git_worktree worker starts denied pushes to the default
// branches, force pushes and merges, and [guards] deny adds to that.
func TestRound11StaticGuards(t *testing.T) {
	x := newToy(t)
	x.write(filepath.Join(x.config, "config.toml"), readFile(t, filepath.Join(x.config, "config.toml"))+"\n[guards]\ndeny = [\"make deploy*\"]\n")
	x.spawn("toy-1", "branch")
	deny := x.deny("toy-1")
	for _, cmd := range []string{"git push origin main", "git push origin HEAD:master", "git push --force origin toy-1", "gh pr merge 3", "make deploy prod"} {
		if _, ok := guards.Denies(deny, cmd); !ok {
			t.Errorf("toy-1 may run %q", cmd)
		}
	}
	if g, ok := guards.Denies(deny, "git push -u origin toy-1"); ok {
		t.Errorf("toy-1 may not push its own branch (%q)", g)
	}
}

// bees-12.21: a branch whose name starts with a default branch's isn't guarded.
func TestPilotBug1221GuardGlobs(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "branch")
	for _, cmd := range []string{"git push origin maintenance", "git push origin toy-1:mainline"} {
		if g, ok := guards.Denies(x.deny("toy-1"), cmd); ok {
			t.Errorf("%q denied by %q", cmd, g)
		}
	}
}

// bees-12.20: allow_merge_into on a repo whose default branch the host doesn't protect is
// refused at bs queen, not first at every spawn; with protection, spawn lifts the merge
// deny and keeps the rest.
func TestPilotBug1220MergeGrantOnAnUnprotectedRepo(t *testing.T) {
	x := newToy(t)
	x.github()
	x.write(filepath.Join(x.config, "config.toml"), readFile(t, filepath.Join(x.config, "config.toml"))+"\n[guards]\nallow_merge_into = [\"stack/*\"]\n")
	x.shell(x.session, "keeper", x.repo)
	x.harness.Step(t, "keeper", "bs", "queen")
	r := x.harness.Await(t, "keeper", 1)
	must(t, result{r.Code, r.Stdout, r.Stderr}, 2, "every spawn would refuse this [guards] table", "protected")
	x.bead("toy-1", "x")
	must(t, x.queen("spawn", "--goal", "branch", "toy-1"), 2, "protected")
	x.gh.Protected = map[string]bool{"main": true}
	x.spawn("toy-1", "branch")
	deny := x.deny("toy-1")
	if g, ok := guards.Denies(deny, "gh pr merge 3"); ok {
		t.Errorf("the merge grant left %q", g)
	}
	if _, ok := guards.Denies(deny, "git push origin main"); !ok {
		t.Error("the merge grant dropped the push guard")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
