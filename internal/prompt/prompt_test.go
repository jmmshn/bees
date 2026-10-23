package prompt_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/prompt"
)

// git wording, as a whole word: what a non-git worker must never read (bees-12.29).
var gitWords = regexp.MustCompile(`(?i)\b(git|branch(es)?|commits?|worktrees?|merged?|push(ed)?|PRs?|pull request)\b`)

// The role prompts and the shared parts name no VCS: a worker in a none or command
// workspace with a goal that isn't git's reads no git wording (bees-12.29).
// The queen's side, every goal type under none, is bees-13.19.
func TestANonGitWorkerReadsNoGitWording(t *testing.T) {
	got := prompt.Text(prompt.Worker, "web2", "claude", "none", []string{"verdict", "note", "beads"})
	if m := gitWords.FindAllString(got, -1); m != nil {
		t.Errorf("worker prompt under none with --goal verdict, note, beads says %q:\n%s", m, got)
	}
}

// Each role reads the shared lead and its own part of each fragment, never the other's.
func TestFragmentsSplitPerRole(t *testing.T) {
	w := prompt.Text(prompt.Worker, "web2", "claude", "git_worktree", []string{"branch"})
	q := prompt.Text(prompt.Queen, "web2", "claude", "git_worktree", goal.BuiltinTypes())
	for _, c := range []struct {
		text string
		has  []string
		not  []string
	}{
		{w, []string{"bs send queen-web2", "## Workspace", "its own git worktree", "Commit everything you hand off", "## Goal\n", "`--goal branch`", "Your goal is your branch"},
			[]string{"## Goals", "bs prune` removes", "merge it into its base"}},
		{q, []string{"queen-web2", "When the keeper says \"merge it\"", "3. `bs prune`", "bs spawn --goal", "## Workspace", "its own git worktree", "bs prune` removes", "## Goals", "merge it into its base", "`--goal pr`:", "`--goal verdict`:"},
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

// Harness-only prose reaches only that harness's sessions, under Known pitfalls before the
// shared lines; a harness with no fragment (omp) reads only the shared ones (bees-13.20).
func TestPitfallsArePerHarness(t *testing.T) {
	const claude, agy, shared = "auto-mode permission denial", "agy's permission prompt", "Calling herdr directly"
	for _, r := range []prompt.Role{prompt.Worker, prompt.Queen} {
		for h, c := range map[string]struct{ has, not []string }{
			"claude": {[]string{claude}, []string{agy}},
			"agy":    {[]string{agy, "denied by `bs guard`"}, []string{claude, "Claude"}},
			"omp":    {nil, []string{claude, agy, "Claude"}},
		} {
			got := prompt.Text(r, "web2", h, "git_worktree", []string{"pr"})
			for _, s := range c.has {
				if i := strings.Index(got, s); i < 0 || i > strings.Index(got, shared) || i < strings.Index(got, "## Known pitfalls") {
					t.Errorf("%s on %s: %q missing or outside Known pitfalls before the shared lines:\n%s", r, h, s, got)
				}
			}
			for _, s := range c.not {
				if strings.Contains(got, s) {
					t.Errorf("%s on %s has %q:\n%s", r, h, s, got)
				}
			}
			if !strings.Contains(got, shared) {
				t.Errorf("%s on %s lacks the shared pitfalls", r, h)
			}
		}
	}
}

// A goal type with no fragment yet still names how to hand it off; each type appears once.
func TestAGoalWithNoFragmentStillSaysBsDone(t *testing.T) {
	got := prompt.Text(prompt.Worker, "web2", "claude", "git_worktree", []string{"cl", "branch", "branch"})
	if !strings.Contains(got, "`--goal cl`: `bs done` checks it") || strings.Count(got, "`--goal branch`:") != 1 {
		t.Fatalf("prompt:\n%s", got)
	}
}

// A verdict or note worker is told where its report goes, never a file (bees-13.7: five
// reports left in scratch/ were lost). A beads worker writes no report: its beads are it.
func TestNonPRGoalsSayWhereTheReportGoes(t *testing.T) {
	for g, want := range map[string]string{
		"verdict": "bs done --answer yes|no --report",
		"note":    "bd update <bead> --append-notes",
		"beads":   "bs bead new --parent <bead>",
	} {
		got := prompt.Text(prompt.Worker, "web2", "claude", "none", []string{g})
		if !strings.Contains(got, want) || (g != "beads" && !strings.Contains(got, "never leave") && !strings.Contains(got, "Never leave")) {
			t.Errorf("--goal %s worker prompt lacks %q or the scratch warning:\n%s", g, want, got)
		}
	}
}

// The goal fragments name no review host's commands; the host fragment does, by role.
func TestGoalFragmentsLeaveCommandsToTheHost(t *testing.T) {
	w := prompt.Text(prompt.Worker, "web2", "claude", "git_worktree", []string{"pr", "push", "review"})
	for _, host := range []string{"gh pr", "github"} {
		if strings.Contains(strings.ToLower(w), host) {
			t.Errorf("a goal fragment names %q:\n%s", host, w)
		}
	}
	hw, hq := prompt.Host(prompt.Worker, "github"), prompt.Host(prompt.Queen, "github")
	if !strings.Contains(hw, "gh pr create") || strings.Contains(hw, "gh pr merge") || !strings.Contains(hq, "gh pr merge") {
		t.Errorf("github's host fragment by role:\nworker %s\nqueen %s", hw, hq)
	}
	if prompt.Host(prompt.Worker, "fakecl") != "" {
		t.Error("a host with no fragment got one")
	}
}

// Every shipped prompt names the command as {{bs}}, never a bare bs, so a rename reaches
// every line (bees-13.9); and a beads goal's worker files through bs bead new, which keeps
// it in the project, never bd create.
func TestPromptsNameTheCommandThroughBs(t *testing.T) {
	bare := regexp.MustCompile("(^|[`(\\s])bs (bead|epic|send|inbox|done|status|spawn|end|prune|goal|check)\\b")
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if bare.MatchString(line) {
				t.Errorf("%s:%d names bs bare: %s", path, i+1, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile("goal/beads.md"); !strings.Contains(string(b), "{{bs}} bead new --parent <bead>") || strings.Contains(string(b), "bd create") {
		t.Errorf("goal/beads.md:\n%s", b)
	}
}
