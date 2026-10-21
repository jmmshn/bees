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
	err := worker.HerdrCLI{}.StartAgent(context.Background(), "w1:p1", "bees-8", "claude")
	if !errors.Is(err, worker.ErrWaiting) {
		t.Fatalf("err = %v, want ErrWaiting", err)
	}
}

func TestHerdrTimeoutIsUnconfirmed(t *testing.T) {
	fakeHerdrBinary(t, "timeout")
	err := worker.HerdrCLI{}.StartAgent(context.Background(), "w1:p1", "bees-8", "claude")
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

// Only herdr not answering is ErrHerdrDown (bees-8.10): its server_not_running code, or a
// herdr that can't run. herdr answering with any other failure is not.
func TestHerdrDownOnlyWhenHerdrDidNotAnswer(t *testing.T) {
	ctx := context.Background()
	fakeHerdrBinary(t, "server_not_running")
	if err := (worker.HerdrCLI{}).RemoveWorktree(ctx, "w1", false); !errors.Is(err, worker.ErrHerdrDown) {
		t.Fatalf("server_not_running: remove err = %v, want ErrHerdrDown", err)
	}
	if _, err := (worker.HerdrCLI{}).Workspaces(ctx); !errors.Is(err, worker.ErrHerdrDown) {
		t.Fatalf("server_not_running: list err = %v, want ErrHerdrDown", err)
	}
	dir := t.TempDir() // a herdr that fails without its envelope: it answered, badly
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte("#!/bin/sh\necho panic >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if err := (worker.HerdrCLI{}).RemoveWorktree(ctx, "w1", false); err == nil || errors.Is(err, worker.ErrHerdrDown) {
		t.Fatalf("no envelope: err = %v, want an error that isn't ErrHerdrDown", err)
	}
	t.Setenv("PATH", t.TempDir()) // no herdr at all
	if _, err := (worker.HerdrCLI{}).Workspaces(ctx); !errors.Is(err, worker.ErrHerdrDown) {
		t.Fatalf("no herdr: err = %v, want ErrHerdrDown", err)
	}
}

func TestProcessInfoOfAClosedPaneIsPaneGone(t *testing.T) {
	fakeHerdrBinary(t, "pane_not_found")
	if _, err := (worker.HerdrCLI{}).ProcessInfo(context.Background(), "w1:p1"); !errors.Is(err, worker.ErrPaneGone) {
		t.Fatalf("err = %v, want ErrPaneGone", err)
	}
}

// The real herdr's process-info, read the way bs reads it, finds this test's own pane:
// its shell, and the process tree this test runs in below it (motif 13).
func TestProcessInfoMatchesRealHerdr(t *testing.T) {
	pane := os.Getenv("HERDR_PANE_ID")
	if pane == "" {
		t.Skip("not in a herdr pane")
	}
	pi, err := (worker.HerdrCLI{}).ProcessInfo(context.Background(), pane)
	if err != nil {
		t.Fatal(err)
	}
	if pi.Shell <= 0 || len(pi.Foreground) == 0 || pi.Foreground[0].PID <= 0 || pi.Foreground[0].Name == "" {
		t.Fatalf("process-info of %s = %+v, want a shell and a named foreground process", pane, pi)
	}
}

// herdr refuses a second agent start with a live agent's name and starts nothing
// (agent_name_taken, probed for ADR-0009): that is a running worker, a fenced twin.
func TestHerdrNameTakenMeansRunning(t *testing.T) {
	fakeHerdrBinary(t, "agent_name_taken")
	if err := (worker.HerdrCLI{}).StartAgent(context.Background(), "w1:p1", "bees-8", "claude"); !errors.Is(err, worker.ErrRunning) {
		t.Fatalf("err = %v, want ErrRunning", err)
	}
}

func TestHerdrOtherErrorsAreNotWaiting(t *testing.T) {
	fakeHerdrBinary(t, "pane_not_found")
	err := worker.HerdrCLI{}.StartAgent(context.Background(), "w1:p1", "bees-8", "claude")
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
	err := worker.HerdrCLI{}.StartAgent(context.Background(), "w1:p1", "bees-8", "claude", "a\nb")
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
