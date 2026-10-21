package prompt_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/prompt"
)

func TestEachRoleNamesTheProjectAndItsJob(t *testing.T) {
	shared := []string{"## Known pitfalls", "auto-mode permission denial", "control characters", "bs bead ls", "bs epic", "bs bead new --parent web2-N"}
	for role, want := range map[prompt.Role][]string{
		prompt.Queen:  {"queen-web2", "bs spawn --label", "--repo", "bs status", "bs end", "only after its PR is merged", "capture-pitfall", "never write \"keeper approved\"", "a push to another branch", "never edit, commit", "answer them yourself", "end your turn", "Never sleep-loop", "LABEL column of `bs status`", "never by a workspace id"},
		prompt.Worker: {"bd show <bead>", "run: bs inbox", "bs send queen-web2", "done: <bead> PR #N", "Leave `bd close` to the queen", "Your branch is yours", "never force-push", "Never install anything onto PATH from your work", "\"this pane\", never a workspace id"},
	} {
		got := text(t, role)
		for _, w := range append(shared, want...) {
			if !strings.Contains(got, w) {
				t.Errorf("%s prompt lacks %q:\n%s", role, w, got)
			}
		}
		if strings.Contains(got, "{{") {
			t.Errorf("%s: unfilled placeholder:\n%s", role, got)
		}
	}
}

// The shared bead rules must not let a worker close its own bead.
func TestNoPromptSaysBdCloseIsFineForEveryone(t *testing.T) {
	for _, role := range []prompt.Role{prompt.Queen, prompt.Worker} {
		if got := text(t, role); strings.Contains(got, "`bd close` on this project's beads are fine") {
			t.Errorf("%s prompt still blesses bd close for everyone", role)
		}
	}
}

func TestFileHoldsTheRolePromptInBsCache(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache) // on every OS, macOS too (TestDirFollowsXDGCacheHome)
	t.Setenv("HOME", t.TempDir())
	for _, role := range []prompt.Role{prompt.Queen, prompt.Worker} {
		for range 2 { // rewritten, not appended, at every launch
			path, err := prompt.File(role, "web2", "", github(t))
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(cache, "bees", "web2-"+string(role)+".md"); path != want {
				t.Fatalf("path = %q, want %q", path, want)
			}
			if b, err := os.ReadFile(path); err != nil || string(b) != text(t, role) {
				t.Fatalf("%s holds %q, %v; want Text(%s, web2, github_pr)", path, b, err, role)
			}
		}
	}
	if fi, err := os.Stat(filepath.Join(cache, "bees")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("cache dir: %v, %v; want 0700, private to the user", fi.Mode(), err)
	}
}

// A relayed answer from the queen is a plain answer; the worker prompt must not
// tell workers to refuse it (bees-1.40).
func TestWorkerDoesNotRefuseRelayedAnswers(t *testing.T) {
	if got := text(t, prompt.Worker); strings.Contains(got, "keeper approved") {
		t.Errorf("worker prompt still tells workers to refuse relayed approvals:\n%s", got)
	}
}

// The prompts ship to every project: a bees bead id must read as a bs bug, and the
// bees repo's own paths belong in its AGENTS.md (bees-1.32).
func TestPromptsHoldNoBeesRepoSpecifics(t *testing.T) {
	bareID := regexp.MustCompile(`bees-\d`)
	for _, role := range []prompt.Role{prompt.Queen, prompt.Worker} {
		got := text(t, role)
		for _, line := range strings.Split(got, "\n") {
			if bareID.MatchString(strings.ReplaceAll(line, "bs bug bees-", "")) {
				t.Errorf("%s prompt cites a bees id not marked as a bs bug: %s", role, line)
			}
		}
		if strings.Contains(got, "internal/prompt") {
			t.Errorf("%s prompt names the bees repo's internal/prompt", role)
		}
	}
}

// The cache dir follows XDG_CACHE_HOME on every OS, macOS too (Go ignores it there), and a
// relative one is an error (ADR-0008).
func TestDirFollowsXDGCacheHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	t.Setenv("HOME", t.TempDir())
	if d, err := prompt.Dir(); err != nil || d != filepath.Join(xdg, "bees") {
		t.Errorf("Dir() = %q, %v; want %s", d, err, filepath.Join(xdg, "bees"))
	}
	t.Setenv("XDG_CACHE_HOME", "rel")
	if _, err := prompt.Dir(); err == nil {
		t.Error("a relative XDG_CACHE_HOME was used")
	}
}

func github(t *testing.T) string {
	t.Helper()
	d, err := prompt.Delivery("github_pr", "")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// text is role's prompt for project web2 under the default delivery, github_pr.
func text(t *testing.T, r prompt.Role) string { return must(t, r, "", github(t)) }

// must is prompt.Text for project web2, failing t on an error.
func must(t *testing.T, r prompt.Role, workspace, delivery string) string {
	t.Helper()
	got, err := prompt.Text(r, "web2", workspace, delivery)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// github_pr's fragment is the PR wording QUEEN.md and WORKER.md held before ADR-0009, word
// for word, each line still in its own role's prompt only (bees-12.3).
func TestGitHubPRDeliveryHoldsTheOldPRWording(t *testing.T) {
	for role, lines := range map[prompt.Role][]string{
		prompt.Worker: {
			"Done: open a PR from your own branch (follow AGENTS.md's push rules), `bs send queen-web2 \"done: <bead> PR #N\"`, then stop.",
			"Leave `bd close` to the queen: it closes your bead once the PR is merged.",
			"Your branch is yours: push to it whenever the work needs it, including new commits to your open PR, with no approval. Never push to main or any other branch, never force-push (merge main in instead), never merge or approve a PR.",
		},
		prompt.Queen: {
			"A worker reports `done: <bead> PR #N`; that means \"ready for review\", and the bead stays open. Merges stay the keeper's. Close it with `bd close <bead>` only after its PR is merged, then `bs end` the worker.",
			"Deleting a stacked PR's base branch closes the PR above it → retarget first: `gh pr edit <n> --base main`.",
		},
	} {
		other := prompt.Queen
		if role == prompt.Queen {
			other = prompt.Worker
		}
		for _, line := range lines {
			if !strings.Contains(text(t, role), line) {
				t.Errorf("%s prompt lacks %q", role, line)
			}
			if strings.Contains(text(t, other), line) {
				t.Errorf("%s prompt holds the %s's %q", other, role, line)
			}
		}
		if got := text(t, role); strings.Contains(got, "<!--") || !strings.Contains(got, "## Delivery\n\nWork lands as a pull request") {
			t.Errorf("%s prompt's delivery section:\n%s", role, got)
		}
	}
}

// The role prompts hold no PR wording of their own: it all comes from the fragment, so a
// manual project's prompts never tell a worker to open a PR.
func TestManualDeliveryMentionsNoPR(t *testing.T) {
	pr := regexp.MustCompile(`\bPRs?\b|gh pr|pull request`)
	for _, role := range []prompt.Role{prompt.Queen, prompt.Worker} {
		if got := must(t, role, "", ""); pr.MatchString(got) {
			t.Errorf("%s prompt without a delivery fragment mentions %q:\n%s", role, pr.FindString(got), got)
		}
	}
	d, err := prompt.Delivery("manual", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []prompt.Role{prompt.Queen, prompt.Worker} {
		got := must(t, role, "", d)
		if pr.MatchString(got) {
			t.Errorf("manual %s prompt mentions %q", role, pr.FindString(got))
		}
		for _, w := range []string{"## Delivery", "done: <bead>", "`bd close`"} {
			if !strings.Contains(got, w) {
				t.Errorf("manual %s prompt lacks %q", role, w)
			}
		}
	}
}

// A fragment file replaces the kind's wording; it is read at every call, and an unreadable
// one is an error naming the key, so the launch stops.
func TestDeliveryFragmentIsTheUsersOwnFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "cl.md")
	if err := os.WriteFile(f, []byte("Mail a CL to {{project}}-reviewers.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := prompt.Delivery("github_pr", f)
	if err != nil {
		t.Fatal(err)
	}
	if got := must(t, prompt.Worker, "", d); !strings.Contains(got, "## Delivery\n\nMail a CL to web2-reviewers.") || strings.Contains(got, "PR #N") {
		t.Errorf("worker prompt with a fragment:\n%s", got)
	}
	os.Remove(f)
	if _, err := prompt.Delivery("github_pr", f); err == nil || !strings.Contains(err.Error(), "delivery.fragment") {
		t.Errorf("missing fragment: %v, want an error naming delivery.fragment", err)
	}
	if _, err := prompt.Delivery("carrier_pigeon", ""); err == nil {
		t.Error("an unknown kind had a fragment")
	}
}

// Under workspace kind none no git wording reaches a worker: no branch to commit on, no
// worktree to make, and the queen hears that workers share the directory (bees-12.28,
// bees-12.29). Under git_worktree, the default, the branch wording is still there.
func TestNoneWorkspaceMentionsNoBranchOrWorktree(t *testing.T) {
	manual, err := prompt.Delivery("manual", "")
	if err != nil {
		t.Fatal(err)
	}
	git := regexp.MustCompile(`your own branch|on this branch|this worktree|branch name|its last commit|bs prune`)
	for role, want := range map[prompt.Role][]string{
		prompt.Worker: {"## Workspace\n\nWorkers have no worktree and no branch", "Never switch branches, make a worktree", "lists the files you made or changed", "done: <bead>"},
		prompt.Queen:  {"workers share it", "touches no file", "leaving its files in the directory"},
	} {
		got := must(t, role, "none", manual)
		if git.MatchString(got) {
			t.Errorf("none %s prompt says %q:\n%s", role, git.FindString(got), got)
		}
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("none %s prompt lacks %q", role, w)
			}
		}
		if got, _ := prompt.Text(role, "web2", "git_worktree", manual); !strings.Contains(got, "own branch") {
			t.Errorf("git_worktree %s prompt lost its branch wording:\n%s", role, got)
		}
	}
	if _, err := prompt.Text(prompt.Worker, "web2", "carrier_pigeon", manual); err == nil {
		t.Error("an unknown workspace kind had a fragment")
	}
}
