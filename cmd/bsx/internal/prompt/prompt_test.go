package prompt_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/jmmshn/bees/cmd/bsx/internal/prompt"
)

// git wording, as a whole word: what a non-git worker must never read (bees-12.29).
var gitWords = regexp.MustCompile(`(?i)\b(git|branch(es)?|commits?|worktrees?|merged?|push(ed)?|PRs?|pull request)\b`)

// The role prompts and the shared parts name no VCS: a worker in a none or command
// workspace with a goal that isn't git's reads no git wording (bees-12.29).
func TestANonGitWorkerReadsNoGitWording(t *testing.T) {
	for _, r := range []prompt.Role{prompt.Worker, prompt.Queen} {
		got := prompt.Text(r, "web2", "none", []string{"verdict"})
		if m := gitWords.FindAllString(got, -1); m != nil {
			t.Errorf("%s prompt under none with --goal verdict says %q:\n%s", r, m, got)
		}
	}
}

// Each role reads the shared lead and its own part of each fragment, never the other's.
func TestFragmentsSplitPerRole(t *testing.T) {
	w := prompt.Text(prompt.Worker, "web2", "git_worktree", []string{"branch"})
	q := prompt.Text(prompt.Queen, "web2", "git_worktree", prompt.Goals())
	for _, c := range []struct {
		text string
		has  []string
		not  []string
	}{
		{w, []string{"bs send queen-web2", "## Workspace", "its own git worktree", "Commit everything you hand off", "## Goal\n", "`--goal branch`", "Your goal is your branch"},
			[]string{"## Goals", "bs prune` removes", "merge it into its base"}},
		{q, []string{"queen-web2", "bs spawn --goal", "## Workspace", "its own git worktree", "bs prune` removes", "## Goals", "merge it into its base"},
			[]string{"Commit everything you hand off", "Your goal is your branch"}},
	} {
		for _, h := range c.has {
			if !strings.Contains(c.text, h) {
				t.Errorf("lacks %q:\n%s", h, c.text)
			}
		}
		for _, n := range c.not {
			if strings.Contains(c.text, n) {
				t.Errorf("has the other role's %q:\n%s", n, c.text)
			}
		}
		if strings.Contains(c.text, "{{") || strings.Contains(c.text, "<!--") {
			t.Errorf("unfilled placeholder or a role marker left:\n%s", c.text)
		}
	}
}

// A goal type with no fragment yet still names how to hand it off; each type appears once.
func TestAGoalWithNoFragmentStillSaysBsDone(t *testing.T) {
	got := prompt.Text(prompt.Worker, "web2", "git_worktree", []string{"verdict", "branch", "branch"})
	if !strings.Contains(got, "`--goal verdict`: `bs done` checks it") || strings.Count(got, "`--goal branch`:") != 1 {
		t.Fatalf("prompt:\n%s", got)
	}
}
