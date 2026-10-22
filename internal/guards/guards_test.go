package guards_test

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/guards"
	"github.com/jmmshn/bees/internal/review/github"
)

// matches is the oracle: a glob over a whole command line as Claude Code's Bash(...) rules
// read it, * any text and nothing else special, written independently of guards.Denies.
func matches(glob, cmd string) bool {
	parts := strings.Split(glob, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^(?s)" + strings.Join(parts, ".*") + "$").MatchString(cmd)
}

func denied(deny []string, cmd string) bool {
	return slices.ContainsFunc(deny, func(g string) bool { return matches(g, cmd) })
}

// worktree is a git_worktree worker of a GitHub repo whose default branch is develop.
var worktree = guards.In{Kind: config.GitWorktree, Git: true, DefaultBranches: guards.DefaultBranches("develop"), MergeDeny: github.Host{}.MergeDeny(), ApproveDeny: github.Host{}.ApproveDeny()}

func build(t *testing.T, g config.Guards, in guards.In) []string {
	t.Helper()
	deny, err := guards.Build(g, in)
	if err != nil {
		t.Fatal(err)
	}
	return deny
}

// The guards matrix (round 11's static guards): each workspace kind's default, against
// the commands a worker must never run and the ones it runs every day.
func TestTheDefaultMatrix(t *testing.T) {
	never := []string{
		"git push origin develop", "git push -u origin web2-3:develop", "git -C /wt push origin HEAD:refs/heads/develop",
		"git push origin main", "git push origin main --tags", "command git push origin master", "git push origin refs/heads/main",
		"/usr/bin/git push --force origin web2-3", "git push origin web2-3 --force-with-lease", "git push -f origin web2-3",
		"git push origin web2-3 -f", "git push origin +web2-3", "git push +web2-3",
		"gh pr merge 41", "gh -R o/r pr merge 41 --squash", "gh api repos/o/r/pulls/41/merge -X PUT",
		"gh pr review 41 --approve", "gh pr review 41 -a",
	}
	daily := []string{
		"git push -u origin web2-3", "git push origin HEAD:web2-3", "git push origin maintenance", "git push origin web2-3:maintenance",
		"git push origin main-fix", "git push origin feature/main", "git commit -m 'push to main --force'",
		"gh pr create --base develop", "gh pr view 41", "gh pr review 41 --comment -b 'looks fine'", "git merge origin/develop",
	}
	deny := build(t, config.Guards{}, worktree)
	for _, cmd := range never {
		if !denied(deny, cmd) {
			t.Errorf("git_worktree lets through %q", cmd)
		}
	}
	for _, cmd := range daily {
		if denied(deny, cmd) {
			t.Errorf("git_worktree refuses the worker's own %q", cmd)
		}
	}
	// none and command in a git checkout: no push to a default branch, no force push, and
	// nothing else.
	for _, kind := range []string{config.NoWorkspace, config.CommandKind} {
		deny := build(t, config.Guards{}, guards.In{Kind: kind, Git: true, DefaultBranches: guards.DefaultBranches(), MergeDeny: github.Host{}.MergeDeny(), ApproveDeny: github.Host{}.ApproveDeny()})
		for _, cmd := range []string{"git push origin main", "git push origin x:master", "git push --force origin x"} {
			if !denied(deny, cmd) {
				t.Errorf("%s lets through %q", kind, cmd)
			}
		}
		for _, cmd := range []string{"gh pr merge 41", "git push origin maintenance"} {
			if denied(deny, cmd) {
				t.Errorf("%s refuses %q", kind, cmd)
			}
		}
		if deny := build(t, config.Guards{}, guards.In{Kind: kind}); len(deny) != 0 {
			t.Errorf("%s outside a git checkout denies %q", kind, deny)
		}
	}
}

// bees-12.21: a branch whose name starts with a default branch's is the worker's own;
// every kind that denies pushes also denies force pushes.
func TestBranchGlobsEndAtAWord(t *testing.T) {
	deny := build(t, config.Guards{}, worktree)
	for _, cmd := range []string{"git push origin maintenance", "git push origin mainline:mainline", "git push -u origin master-docs"} {
		if g, ok := guards.Denies(deny, cmd); ok {
			t.Errorf("%q denied by %q", cmd, g)
		}
	}
	if g, ok := guards.Denies(deny, "git push --force origin maintenance"); !ok {
		t.Errorf("a force push allowed (%q)", g)
	}
}

// Denies and the oracle agree on every glob the default builds, over the matrix's lines.
func TestDeniesAgreesWithTheOracle(t *testing.T) {
	deny := build(t, config.Guards{}, worktree)
	for _, cmd := range []string{"git push origin main", "git push origin maintenance", "gh pr merge 1", "git push -f", "x", ""} {
		if _, ok := guards.Denies(deny, cmd); ok != denied(deny, cmd) {
			t.Errorf("%q: Denies %v, oracle %v", cmd, ok, !ok)
		}
	}
}

// [guards] deny adds to the default; replace = true replaces it.
func TestDenyAddsAndReplaceReplaces(t *testing.T) {
	deny := build(t, config.Guards{Deny: []string{"g4 submit*"}}, worktree)
	if !denied(deny, "g4 submit 12") || !denied(deny, "git push origin main") {
		t.Errorf("deny = %q: want the default plus g4 submit*", deny)
	}
	deny = build(t, config.Guards{Deny: []string{"g4 submit*"}, Replace: true}, worktree)
	if !slices.Equal(deny, []string{"g4 submit*"}) {
		t.Errorf("replace: deny = %q", deny)
	}
	for _, bad := range []string{"", "git push\nmain"} {
		if _, err := guards.Build(config.Guards{Deny: []string{bad}}, worktree); err == nil {
			t.Errorf("deny %q accepted", bad)
		}
	}
}

// bees-12.20: allow_merge_into lifts the merge deny only where GitHub protects the default
// branch, never for a glob that matches one, and only for a workspace with a branch.
func TestAllowMergeInto(t *testing.T) {
	grant := config.Guards{AllowMergeInto: []string{"pilot/*"}}
	if _, err := guards.Build(grant, worktree); !errors.Is(err, guards.ErrUnprotected) {
		t.Errorf("a grant on an unprotected repo: %v, want ErrUnprotected", err)
	}
	in := worktree
	in.Protected = true
	deny := build(t, grant, in)
	if denied(deny, "gh pr merge 41") {
		t.Error("allow_merge_into left gh pr merge denied")
	}
	for _, cmd := range []string{"git push origin develop", "gh pr review 41 --approve", "git push --force origin x"} {
		if !denied(deny, cmd) {
			t.Errorf("allow_merge_into let through %q", cmd)
		}
	}
	for _, glob := range []string{"*", "dev*", "main", "[", "ma?ter"} {
		if _, err := guards.Build(config.Guards{AllowMergeInto: []string{glob}}, in); err == nil {
			t.Errorf("allow_merge_into %q accepted", glob)
		}
	}
	if err := guards.Check(grant, guards.In{Kind: config.NoWorkspace, Git: true, Protected: true, DefaultBranches: guards.DefaultBranches()}); err == nil {
		t.Error("allow_merge_into accepted for a workspace with no branch")
	}
}
