package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/harness/claude/claudetest"
	"github.com/jmmshn/bees/internal/worker"
)

// fakeHerdrBinary puts a herdr on PATH that prints herdr's JSON error envelope with
// code and exits 1, as herdr does.
func fakeHerdrBinary(t *testing.T, code string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho '{\"error\":{\"code\":\"" + code + "\",\"message\":\"from the fake\"}}' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestHerdrAgentNotReadyMeansWaiting(t *testing.T) {
	fakeHerdrBinary(t, "agent_not_ready")
	err := worker.HerdrCLI{}.StartClaude(context.Background(), "w1:p1", "bees-8")
	if !errors.Is(err, worker.ErrWaiting) {
		t.Fatalf("err = %v, want ErrWaiting", err)
	}
}

func TestHerdrTimeoutIsUnconfirmed(t *testing.T) {
	fakeHerdrBinary(t, "timeout")
	err := worker.HerdrCLI{}.StartClaude(context.Background(), "w1:p1", "bees-8")
	if !errors.Is(err, worker.ErrUnconfirmed) || errors.Is(err, worker.ErrWaiting) {
		t.Fatalf("err = %v, want ErrUnconfirmed", err)
	}
}

func TestHerdrDirtyWorktreeNeedsForce(t *testing.T) {
	fakeHerdrBinary(t, "dirty_worktree_requires_force")
	if err := (worker.HerdrCLI{}).RemoveWorktree(context.Background(), "w1", false); !errors.Is(err, worker.ErrDirty) {
		t.Fatalf("err = %v, want ErrDirty", err)
	}
}

func TestClaudeSessionsAliveReadsClaudesRegistry(t *testing.T) {
	reg := claudetest.FakeRegistry(t)
	reg.Add(t, "bees-8", "")
	for name, want := range map[string]bool{"bees-8": true, "bees-9": false} {
		if got, err := (worker.ClaudeSessions{}).Alive(context.Background(), name, 300*time.Millisecond); err != nil || got != want {
			t.Errorf("Alive(%s) = %v, %v; want %v", name, got, err, want)
		}
	}
}

func TestHerdrOtherErrorsAreNotWaiting(t *testing.T) {
	fakeHerdrBinary(t, "pane_not_found")
	err := worker.HerdrCLI{}.StartClaude(context.Background(), "w1:p1", "bees-8")
	if err == nil || errors.Is(err, worker.ErrWaiting) {
		t.Fatalf("err = %v, want a plain error", err)
	}
}

// agentArgCases are arguments herdr 0.9.3's agent start was probed with (bees-1.14):
// it refuses any Unicode control character and passes everything else through.
var agentArgCases = map[string]bool{
	"plain": true, "": true, "-x": true, `'quotes" $dollar` + "`tick`": true, "é—🐝": true,
	"line separator": true, `{"crossSessionInbound":"accept"}`: true,
	"new\nline": false, "carriage\rreturn": false, "tab\there": false, "esc\x1b": false,
	"del\x7f": false, "c1\u0085next": false, "c1\u009bcsi": false,
}

func TestCheckAgentArgs(t *testing.T) {
	for arg, ok := range agentArgCases {
		err := worker.CheckAgentArgs([]string{"--name", "bees-8", arg})
		if (err == nil) != ok || (err != nil && !errors.Is(err, worker.ErrUnsafeArg)) {
			t.Errorf("CheckAgentArgs(%q) = %v, want ok=%v", arg, err, ok)
		}
	}
}

// herdr types the launch line into the pane's shell; one that lands before the shell's line
// editor is up goes through the tty's canonical queue, which drops what passes MAX_CANON,
// newline included (1024 bytes on macOS, probed with herdr 0.9.3: bees-8.3).
func TestCheckAgentArgsRefusesALaunchTheTtyWouldCut(t *testing.T) {
	long := strings.Repeat("x", 1024)
	if err := worker.CheckAgentArgs([]string{"--name", "bees-8", long}); !errors.Is(err, worker.ErrLongLaunch) {
		t.Fatalf("err = %v, want ErrLongLaunch", err)
	}
	if err := worker.CheckAgentArgs([]string{"--name", "bees-8", long[:500]}); err != nil {
		t.Fatalf("500 bytes: %v", err)
	}
}

func TestStartClaudeRefusesUnsafeArgsWithoutRunningHerdr(t *testing.T) {
	fakeHerdrBinary(t, "herdr_was_run")
	err := worker.HerdrCLI{}.StartClaude(context.Background(), "w1:p1", "bees-8", "a\nb")
	if !errors.Is(err, worker.ErrUnsafeArg) || strings.Contains(err.Error(), "herdr_was_run") {
		t.Fatalf("err = %v, want ErrUnsafeArg before herdr runs", err)
	}
}

// TestCheckAgentArgsMatchesRealHerdr holds CheckAgentArgs, and so fakeHerdr, to the
// installed herdr. herdr checks agent arguments before it looks up the pane, so a pane
// that doesn't exist makes this side-effect free. Skipped without a running herdr.
func TestCheckAgentArgsMatchesRealHerdr(t *testing.T) {
	if exec.Command("herdr", "workspace", "list").Run() != nil {
		t.Skip("no running herdr")
	}
	for arg, ok := range agentArgCases {
		out, _ := exec.Command("herdr", "agent", "start", "bs-contract", "--kind", "claude", "--pane", "bs-contract-no-such-pane", "--", arg).CombinedOutput()
		var he struct{ Error struct{ Code string } }
		if json.Unmarshal(out, &he) != nil {
			t.Fatalf("herdr output for %q: %s", arg, out)
		}
		if refused := he.Error.Code == "invalid_agent_argument"; refused == ok || (ok && he.Error.Code != "agent_pane_not_found") {
			t.Errorf("herdr on %q: %s; CheckAgentArgs says ok=%v", arg, he.Error.Code, ok)
		}
	}
}

// TestHerdrKeepsLabelsVerbatim holds fakeHerdr to the installed herdr: a workspace
// label is stored as given, whatever its length or characters, so a bad label is
// bs's to refuse (CheckLabel). Opens and closes a throwaway workspace per label;
// skipped without a running herdr.
func TestHerdrKeepsLabelsVerbatim(t *testing.T) {
	if exec.Command("herdr", "workspace", "list").Run() != nil {
		t.Skip("no running herdr")
	}
	for _, label := range []string{"role prompts", "", "new\nline", strings.Repeat("a", 200), `é—🐝 "quoted".`} {
		out, err := exec.Command("herdr", "workspace", "create", "--cwd", t.TempDir(), "--label", label, "--no-focus").Output()
		var created struct {
			Result struct {
				Workspace struct {
					ID string `json:"workspace_id"`
				} `json:"workspace"`
			} `json:"result"`
		}
		if err != nil || json.Unmarshal(out, &created) != nil || created.Result.Workspace.ID == "" {
			t.Fatalf("herdr workspace create --label %q: %v: %s", label, err, out)
		}
		id := created.Result.Workspace.ID
		out, err = exec.Command("herdr", "workspace", "get", id).Output()
		_ = exec.Command("herdr", "workspace", "close", id).Run()
		var got struct {
			Result struct {
				Workspace struct{ Label string } `json:"workspace"`
			} `json:"result"`
		}
		if err != nil || json.Unmarshal(out, &got) != nil {
			t.Fatalf("herdr workspace get %s: %v: %s", id, err, out)
		}
		if got.Result.Workspace.Label != label {
			t.Errorf("herdr stored label %q as %q", label, got.Result.Workspace.Label)
		}
	}
}
