package claude_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/harness/claude"
	"github.com/jmmshn/bees/internal/prompt"
)

// workerSettings returns the --settings JSON Prepare renders for a worker started with st.
func workerSettings(t *testing.T, st harness.Start) map[string]json.RawMessage {
	t.Helper()
	st.Role, st.Name = prompt.Worker, "bees-8"
	l, err := claude.New(config.ClaudeCode{}).Prepare(st)
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
	s := workerSettings(t, harness.Start{Path: []string{a, b}})
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
	if b, _ := os.ReadFile(envFile); strings.Contains(string(b), "/usr/bin:/bin\"") {
		t.Errorf("env file %q pins the hook's PATH, not the Bash tool's", b)
	}
}

func TestNoPathNoHook(t *testing.T) {
	if s := workerSettings(t, harness.Start{}); s["hooks"] != nil {
		t.Fatalf("hooks = %s, want none without a Path", s["hooks"])
	}
}
