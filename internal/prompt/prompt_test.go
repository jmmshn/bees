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
		prompt.Worker: {"bd show <bead>", "run: bs inbox", "bs send queen-web2", "done: <bead> PR #N", "Leave `bd close` to the queen", "Your branch is yours", "never force-push", "Never install anything onto PATH from your branch", "\"this pane\", never a workspace id"},
	} {
		got := prompt.Text(role, "web2")
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
		if got := prompt.Text(role, "web2"); strings.Contains(got, "`bd close` on this project's beads are fine") {
			t.Errorf("%s prompt still blesses bd close for everyone", role)
		}
	}
}

func TestFileHoldsTheRolePromptInBsCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // macOS ignores XDG_CACHE_HOME for ~/Library/Caches
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []prompt.Role{prompt.Queen, prompt.Worker} {
		for range 2 { // rewritten, not appended, at every launch
			path, err := prompt.File(role, "web2")
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(cache, "bees", "web2-"+string(role)+".md"); path != want {
				t.Fatalf("path = %q, want %q", path, want)
			}
			if b, err := os.ReadFile(path); err != nil || string(b) != prompt.Text(role, "web2") {
				t.Fatalf("%s holds %q, %v; want Text(%s, web2)", path, b, err, role)
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
	if got := prompt.Text(prompt.Worker, "web2"); strings.Contains(got, "keeper approved") {
		t.Errorf("worker prompt still tells workers to refuse relayed approvals:\n%s", got)
	}
}

// The prompts ship to every project: a bees bead id must read as a bs bug, and the
// bees repo's own paths belong in its AGENTS.md (bees-1.32).
func TestPromptsHoldNoBeesRepoSpecifics(t *testing.T) {
	bareID := regexp.MustCompile(`bees-\d`)
	for _, role := range []prompt.Role{prompt.Queen, prompt.Worker} {
		got := prompt.Text(role, "web2")
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
