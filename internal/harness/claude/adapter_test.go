package claude_test

import (
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/harness/claude"
	"github.com/jmmshn/bees/internal/port"
)

// workerSettings returns the --settings JSON Prepare renders for a worker started with st.
func workerSettings(t *testing.T, st port.Start) map[string]json.RawMessage {
	t.Helper()
	st.Role, st.Name = port.Worker, "bees-8"
	l, err := claude.Adapter{}.Prepare(st)
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]json.RawMessage
	if err := json.Unmarshal([]byte(l.Files["settings.json"]), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Shape C of bees-8.19's PATH probe: Claude's Bash tool sources CLAUDE_ENV_FILE after the
// shell profile, so a SessionStart hook appending an export puts Path first on the PATH
// every Bash call sees, built on the pane's own PATH. Run the hook as Claude would, then
// source the env file as the Bash tool does.
func TestPathGoesFirstOnTheBashToolsPath(t *testing.T) {
	a, b := t.TempDir(), filepath.Join(t.TempDir(), "with space")
	s := workerSettings(t, port.Start{Path: []string{a, b}})
	var hooks map[string][]struct {
		Hooks []struct{ Type, Command string }
	}
	if err := json.Unmarshal(s["hooks"], &hooks); err != nil || len(hooks["SessionStart"]) != 1 || len(hooks["SessionStart"][0].Hooks) != 1 {
		t.Fatalf("hooks = %s, %v; want one SessionStart hook", s["hooks"], err)
	}
	h := hooks["SessionStart"][0].Hooks[0]
	envFile := filepath.Join(t.TempDir(), "env.sh")
	run := exec.Command("sh", "-c", h.Command)
	run.Env = []string{"PATH=/usr/bin:/bin", "CLAUDE_ENV_FILE=" + envFile}
	if out, err := run.CombinedOutput(); h.Type != "command" || err != nil {
		t.Fatalf("hook %+v: %v %s", h, err, out)
	}
	got, err := exec.Command("/bin/sh", "-c", `PATH=/pane/bin:/usr/bin:/bin; . "$1"; printf %s "$PATH"`, "sh", envFile).Output()
	if want := a + ":" + b + ":/pane/bin:/usr/bin:/bin"; err != nil || string(got) != want {
		t.Fatalf("PATH after the env file = %q, %v; want %q", got, err, want)
	}
}

// The adapter owns its quoting: a dir its export can't hold is refused, never written.
func TestPrepareRefusesADirTheExportCantQuote(t *testing.T) {
	for _, d := range []string{"/a'b", `/a"b`, "/$HOME", "/a`b", `/a\b`, "/a:b", "/a\nb"} {
		_, err := claude.Adapter{}.Prepare(port.Start{Role: port.Worker, Name: "bees-8", Path: []string{d}})
		if !errors.Is(err, claude.ErrBadPath) {
			t.Errorf("Path %q: err = %v, want ErrBadPath", d, err)
		}
	}
}

func TestNoPathNoHook(t *testing.T) {
	if s := workerSettings(t, port.Start{}); s["hooks"] != nil {
		t.Fatalf("hooks = %s, want none without a Path", s["hooks"])
	}
}

// The argv: model first as one argument, --continue only when asked, the settings and
// prompt as the core's {{key}} placeholders, the doorbell last; no settings for a queen.
func TestPrepareBuildsTheArgv(t *testing.T) {
	l, err := claude.Adapter{Model: "sonnet"}.Prepare(port.Start{Role: port.Worker, Name: "bees-8", PromptFile: "{{prompt.md}}", Doorbell: "[bs] hi", Deny: []string{"git push *main*"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--model=sonnet", "--name", "bees-8", "--settings", "{{settings.json}}", "--append-system-prompt-file", "{{prompt.md}}", "[bs] hi"}
	if !slices.Equal(l.Args, want) {
		t.Fatalf("args = %q, want %q", l.Args, want)
	}
	if !strings.Contains(l.Files["settings.json"], `"deny":["Bash(git push *main*)"]`) || !strings.Contains(l.Files["settings.json"], `"crossSessionInbound":"accept"`) {
		t.Fatalf("settings = %s", l.Files["settings.json"])
	}
	l, err = claude.Adapter{}.Prepare(port.Start{Role: port.Queen, Name: "queen-bees", Continue: true})
	if err != nil || !slices.Equal(l.Args, []string{"--continue", "--name", "queen-bees"}) || len(l.Files) != 0 {
		t.Fatalf("queen: %q %v %v", l.Args, l.Files, err)
	}
}

// A worker's shell calls run as long as bs done --wait needs, by default too: Claude
// Code's own default (2 minutes) would cut a handoff's wait (eng-review of #108).
func TestShellTimeoutIsTheWorkersEnv(t *testing.T) {
	s := workerSettings(t, port.Start{ShellTimeout: 10 * time.Minute})
	var env map[string]string
	if err := json.Unmarshal(s["env"], &env); err != nil {
		t.Fatal(err)
	}
	if env["BASH_DEFAULT_TIMEOUT_MS"] != "600000" || env["BASH_MAX_TIMEOUT_MS"] != "600000" || len(env) != 2 {
		t.Fatalf("env = %v, want both bash timeouts at 600000 and nothing else", env)
	}
	if s := workerSettings(t, port.Start{}); s["env"] != nil {
		t.Fatalf("env = %s, want none without a ShellTimeout", s["env"])
	}
}

// A worker's autoMode says whose worktree and branch these are and lets its routine work
// through (bees-1.51): the rebuild shipped only $defaults and the classifier gated the
// worker's own push and gh pr create (bees-13.72). Pinned word for word, so a rewrite
// can't drop it silently again.
func TestAutoModeKnowsTheWorkersOwnBranch(t *testing.T) {
	autoMode := func(st port.Start) map[string][]string {
		var a map[string][]string
		if err := json.Unmarshal(workerSettings(t, st)["autoMode"], &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	build := "Running this repository's own build, lint and test commands (such as go vet, go test, go build -o into the worktree, pre-commit) inside the worktree /wt/bees-8."
	tools := "Running bs and bd, the bees tools, to read and send its project's messages and to read and update its own beads."
	want := map[string][]string{
		"environment": {"$defaults", "Host containment: a bees worker, working in its own git worktree /wt/bees-8 on its own branch bees-8 of the repository /repo."},
		"allow": {"$defaults", build,
			"Committing to and pushing (never force-pushing) the worker's own branch bees-8 to the repository's origin, opening a pull request from it, and pushing further commits to that open pull request.",
			tools},
	}
	if got := autoMode(port.Start{Dir: "/wt/bees-8", Branch: "bees-8", Repo: "/repo"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("autoMode = %q,\nwant %q", got, want)
	}
	// A push goal's branch is pushed onto its target's head, not opened as a change (bees-13.78.1).
	want["environment"] = []string{"$defaults", "Host containment: a bees worker, working in its own git worktree /wt/bees-8 on its own branch bees-8 of the repository /repo; its goal is to push that branch onto branch feature, the head of a pull request it was handed."}
	want["allow"] = []string{"$defaults", build,
		"Committing to the worker's own branch bees-8 and pushing it (never force-pushing) onto branch feature, the head of the pull request its goal names, on the remote that holds that head (git push <remote> HEAD:feature), and pushing further commits there.",
		tools}
	if got := autoMode(port.Start{Dir: "/wt/bees-8", Branch: "bees-8", PushTo: "feature", Repo: "/repo"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("push: autoMode = %q,\nwant %q", got, want)
	}
	// A workspace with no branch of its own claims none and pushes none.
	want = map[string][]string{
		"environment": {"$defaults", "Host containment: a bees worker, working in its own git worktree /wt/bees-8 of the repository /repo."},
		"allow":       {"$defaults", build, tools},
	}
	if got := autoMode(port.Start{Dir: "/wt/bees-8", Repo: "/repo"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("no branch: autoMode = %q,\nwant %q", got, want)
	}
}
