package guards_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/guards"
)

// matches is a guard's glob over a command line: * matches any text, nothing else is special.
func matches(glob, cmd string) bool {
	parts := strings.Split(glob, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$").MatchString(cmd)
}

func denied(r guards.Rules, cmd string) bool {
	return slices.ContainsFunc(r.Deny, func(g string) bool { return matches(g, cmd) })
}

var w = guards.Worker{Branch: "web2-3", Worktree: "/wt/web2-3", DefaultBranches: guards.DefaultBranches("develop")}

func build(t *testing.T, kind string, g config.Guards) guards.Rules {
	t.Helper()
	r, err := guards.Build(config.Delivery{Kind: kind}, g, w)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// github_pr's default is today's push and merge matrix, against the repo's real default
// branch as well as main and master (bees-11.7).
func TestGitHubPRDefaultDeniesPushesToEveryDefaultBranchAndMerges(t *testing.T) {
	r := build(t, "github_pr", config.Guards{})
	for _, cmd := range []string{
		"git push origin develop", "git push -u origin web2-3:develop", "git -C /wt push origin HEAD:refs/heads/develop",
		"git push origin main", "command git push origin master", "/usr/bin/git push --force origin web2-3",
		"git push origin +web2-3", "gh pr merge 41", "gh -R o/r pr merge 41", "gh pr review 41 --approve", "gh api repos/o/r/pulls/41/merge",
	} {
		if !denied(r, cmd) {
			t.Errorf("default guards let through %q", cmd)
		}
	}
	for _, cmd := range []string{"git push -u origin web2-3", "git commit -m 'push to develop'", "gh pr create --base develop"} {
		if denied(r, cmd) {
			t.Errorf("default guards refuse the worker's own %q", cmd)
		}
	}
	if allow := strings.Join(r.Allow, " "); !strings.Contains(allow, "web2-3") || !strings.Contains(allow, "/wt/web2-3") || !strings.Contains(allow, "pull request") {
		t.Errorf("allow = %q, want the branch, the worktree and its pull request", r.Allow)
	}
}

// For a repo whose default is main, the default matrix is exactly the one bs shipped
// before the guards moved to config: no behaviour change for a keeper who sets nothing.
func TestGitHubPRDefaultForMainIsTheOldMatrix(t *testing.T) {
	var old []string
	for _, git := range []string{"git", "command git", "*/git"} {
		for _, push := range []string{git + " push", git + " -* push"} {
			for _, bad := range []string{"--force*", "-f*", "+*", "main*", "master*", "*:main*", "*:master*", "*refs/heads/main*", "*refs/heads/master*"} {
				old = append(old, push+" "+bad)
				if !strings.HasPrefix(bad, "*") {
					old = append(old, push+" * "+bad)
				}
			}
		}
	}
	old = append(old, "gh pr merge*", "gh * pr merge*", "gh pr review* --approve*", "gh pr review* -a*", "gh api *merge*")
	r, err := guards.Build(config.Delivery{Kind: "github_pr"}, config.Guards{}, guards.Worker{DefaultBranches: guards.DefaultBranches("main")})
	if err != nil || !slices.Equal(r.Deny, old) {
		t.Errorf("deny = %q, %v; want the old matrix %q", r.Deny, err, old)
	}
}

// manual's default only keeps pushes off the default branch; it says nothing of PRs.
func TestManualDefaultDeniesOnlyPushesToTheDefaultBranch(t *testing.T) {
	r := build(t, "manual", config.Guards{})
	for _, cmd := range []string{"git push origin develop", "git push origin web2-3:main"} {
		if !denied(r, cmd) {
			t.Errorf("manual guards let through %q", cmd)
		}
	}
	if denied(r, "git push -u origin web2-3") {
		t.Error("manual guards refuse the worker's own push")
	}
	if all := strings.Join(append(r.Deny, r.Allow...), " "); strings.Contains(all, "gh ") || strings.Contains(all, "pull request") {
		t.Errorf("manual guards mention GitHub: %q", all)
	}
}

// A configured deny list replaces the default.
func TestConfiguredDenyReplacesTheDefault(t *testing.T) {
	r := build(t, "github_pr", config.Guards{Deny: []string{"g4 submit*"}})
	if !slices.Equal(r.Deny, []string{"g4 submit*"}) {
		t.Errorf("deny = %q, want only the configured glob", r.Deny)
	}
	for _, bad := range []string{"", "git push\nmain"} {
		if _, err := guards.Build(config.Delivery{Kind: "github_pr"}, config.Guards{Deny: []string{bad}}, w); err == nil {
			t.Errorf("deny %q accepted", bad)
		}
	}
}

// allow_merge_into lifts the merge deny for those bases and says so in prose, but never for
// the default branch, and only where GitHub protects it (bees-12.13).
func TestAllowMergeIntoGrantsScopedMergesOnly(t *testing.T) {
	if _, err := guards.Build(config.Delivery{Kind: "github_pr"}, config.Guards{AllowMergeInto: []string{"pilot/*"}}, w); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Errorf("a merge grant on an unprotected default branch: %v, want refused", err)
	}
	w := w
	w.Protected = true
	r, err := guards.Build(config.Delivery{Kind: "github_pr"}, config.Guards{AllowMergeInto: []string{"pilot/*"}}, w)
	if err != nil {
		t.Fatal(err)
	}
	if denied(r, "gh pr merge 41") {
		t.Error("allow_merge_into left gh pr merge denied")
	}
	for _, cmd := range []string{"git push origin develop", "gh pr review 41 --approve", "gh api repos/o/r/pulls/41/merge"} {
		if !denied(r, cmd) {
			t.Errorf("allow_merge_into let through %q", cmd)
		}
	}
	if allow := strings.Join(r.Allow, " "); !strings.Contains(allow, "pilot/*") || !strings.Contains(allow, "never a pull request into main or master or develop") {
		t.Errorf("allow = %q, want the grant and the default branches it never covers", r.Allow)
	}
	pilot, gh := config.Guards{AllowMergeInto: []string{"pilot/*"}}, config.Delivery{Kind: "github_pr"}
	if note := guards.Note(gh, pilot); !strings.Contains(note, "`pilot/*`") {
		t.Errorf("note = %q", note)
	}
	if note := guards.Note(gh, config.Guards{}); note != "" {
		t.Errorf("note without a grant = %q", note)
	}
	// manual has no PRs: a merge grant there is a mistake, never a line telling workers to merge.
	if _, err := guards.Build(config.Delivery{Kind: "manual"}, pilot, w); err == nil {
		t.Error("allow_merge_into accepted under manual")
	}
	if note := guards.Note(config.Delivery{Kind: "manual"}, pilot); note != "" {
		t.Errorf("manual note = %q", note)
	}
	for _, glob := range []string{"*", "dev*", "main", "[", "ma?ter"} {
		if _, err := guards.Build(config.Delivery{Kind: "github_pr"}, config.Guards{AllowMergeInto: []string{glob}}, w); err == nil {
			t.Errorf("allow_merge_into %q accepted: it matches a default branch, or is no glob", glob)
		}
	}
}

// Denies matches the way the test's own regexp does, so every harness rendering the same
// globs denies the same commands.
func TestDeniesAgreesWithTheGlobOracle(t *testing.T) {
	r := build(t, "github_pr", config.Guards{Deny: []string{"git push *main*", "gh pr merge*", "a*b*a", "exact", "x**y"}})
	for _, cmd := range []string{"git push origin main", "git push main", "gh pr merge", "gh pr merge 4", "aba", "abba", "ab", "exact", "exactly", "xy", "x y", "", "git push origin web"} {
		glob, got := r.Denies(cmd)
		if want := denied(r, cmd); got != want {
			t.Errorf("Denies(%q) = %v (%q), oracle says %v", cmd, got, glob, want)
		}
	}
}
