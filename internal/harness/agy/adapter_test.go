package agy_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/harness/agy"
	"github.com/jmmshn/bees/internal/port"
)

func worker() port.Start {
	return port.Start{Role: port.Worker, Name: "bees-8", Dir: "/wt/bees-8", PromptFile: "{{prompt.md}}", Prompt: "You are a worker.\n",
		Doorbell: "run: bs inbox", Deny: []string{"git push *main*", "it's *"}}
}

// A worker's launch adds its per-start dir, picks its custom agent, runs in auto mode and
// gets the doorbell as its first prompt; the agent file lists the worker's tools and holds
// the prompt as its body (probe 1).
func TestPrepareAWorker(t *testing.T) {
	l, err := agy.Adapter{Model: "gemini-3.8-flash", Bs: "/bin/bs"}.Prepare(worker())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--add-dir", "{{dir}}", "--agent", "bs-worker", "--mode", "auto", "--model=gemini-3.8-flash", "-i", "run: bs inbox"}
	if !reflect.DeepEqual(l.Args, want) {
		t.Errorf("args = %q, want %q", l.Args, want)
	}
	if len(l.Files) != 0 || len(l.Dir) != 2 {
		t.Errorf("files %v, dir %v: want only the agent file and hooks.json in the dir", l.Files, l.Dir)
	}
	agent := l.Dir[".agents/agents/bs-worker.md"]
	front := "---\nname: bs-worker\ndescription: a bees worker, started by bs\ntools: [" + strings.Join(agy.Tools, ", ") + "]\n---\n"
	if agent != front+"You are a worker.\n" {
		t.Errorf("agent file = %q", agent)
	}
	for _, tool := range []string{"run_command", "view_file", "list_dir", "grep_search", "find_by_name", "write_to_file", "replace_file_content", "multi_replace_file_content"} {
		if !strings.Contains(agent, tool) {
			t.Errorf("agent file has no %s", tool)
		}
	}
}

// The queen's launch has her agent and no hooks: guards are a worker's.
func TestPrepareTheQueen(t *testing.T) {
	l, err := agy.Adapter{}.Prepare(port.Start{Role: port.Queen, Name: "queen-bees", Prompt: "You are the queen.\n"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--add-dir", "{{dir}}", "--agent", "bs-queen", "--mode", "auto"}; !reflect.DeepEqual(l.Args, want) {
		t.Errorf("args = %q, want %q", l.Args, want)
	}
	if _, ok := l.Dir[".agents/hooks.json"]; ok || !strings.HasSuffix(l.Dir[".agents/agents/bs-queen.md"], "---\nYou are the queen.\n") {
		t.Errorf("dir = %v", l.Dir)
	}
}

// hooks.json runs bs guard --harness agy before each run_command, with each deny glob one
// argument however it is spelled, under sh -c as agy runs it.
func TestHooksRunBsGuardWithTheGlobs(t *testing.T) {
	bin := t.TempDir()
	bs := filepath.Join(bin, "my bs")
	if err := os.WriteFile(bs, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := agy.Adapter{Bs: bs}.Prepare(worker())
	if err != nil {
		t.Fatal(err)
	}
	var h map[string]struct {
		PreToolUse []struct {
			Matcher string
			Hooks   []struct{ Type, Command string }
		}
	}
	if err := json.Unmarshal([]byte(l.Dir[".agents/hooks.json"]), &h); err != nil {
		t.Fatal(err)
	}
	pre := h["bs-guards"].PreToolUse
	if len(pre) != 1 || pre[0].Matcher != "run_command" || len(pre[0].Hooks) != 1 || pre[0].Hooks[0].Type != "command" {
		t.Fatalf("hooks.json = %s", l.Dir[".agents/hooks.json"])
	}
	out, err := exec.Command("sh", "-c", pre[0].Hooks[0].Command).Output()
	if want := "guard\n--harness\nagy\ngit push *main*\nit's *\n"; err != nil || string(out) != want {
		t.Errorf("the hook ran with %q (%v), want %q", out, err, want)
	}
}

// A resume continues the recorded conversation; with none recorded it is refused, never
// agy -c, which falls back to another conversation.
func TestPrepareResumesOnlyARecordedConversation(t *testing.T) {
	st := worker()
	st.Continue = true
	if _, err := (agy.Adapter{Bs: "/bin/bs"}).Prepare(st); err == nil || !strings.Contains(err.Error(), "no recorded conversation") {
		t.Errorf("a resume with no conversation: %v", err)
	}
	st.Record = map[string]string{agy.Conversation: "7d33c4b2"}
	l, err := agy.Adapter{Bs: "/bin/bs"}.Prepare(st)
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(l.Args, " "); !strings.Contains(s, "--conversation 7d33c4b2") || strings.Contains(s, " -c ") {
		t.Errorf("args = %q", l.Args)
	}
	if l.Record[agy.Conversation] != "7d33c4b2" {
		t.Errorf("record = %v", l.Record)
	}
}

// PATH dirs, which agy can't take yet, and a bs its hook can't find refuse the start
// rather than run a worker without them.
func TestPrepareRefuses(t *testing.T) {
	st := worker()
	st.Path = []string{"/wt/bin"}
	if _, err := (agy.Adapter{Bs: "/bin/bs"}).Prepare(st); err == nil || !strings.Contains(err.Error(), "/wt/bin") {
		t.Errorf("a PATH dir: %v", err)
	}
	for _, bs := range []string{"", "bs"} {
		if _, err := (agy.Adapter{Bs: bs}).Prepare(worker()); err == nil {
			t.Errorf("bs %q: started", bs)
		}
	}
}

// A process is a session's own when it is the configured agy adding that session's
// per-start dir, <name>-dir-<digits>: never another session's whose name extends it.
func TestOwns(t *testing.T) {
	argv := func(bin, dir string) []string {
		return []string{bin, "--add-dir", "/c/bees/bs/" + dir, "--agent", "bs-worker"}
	}
	for _, c := range []struct {
		a    agy.Adapter
		argv []string
		want bool
	}{
		{agy.Adapter{}, argv("agy", "bees-8-dir-123"), true},
		{agy.Adapter{}, argv("/home/k/.local/bin/agy", "bees-8-dir-123"), true},
		{agy.Adapter{}, argv("agy", "bees-8-x-dir-123"), false},
		{agy.Adapter{}, argv("agy", "bees-8-dir-dir-123"), false},
		{agy.Adapter{}, argv("agy", "bees-8-dir-"), false},
		{agy.Adapter{}, argv("claude", "bees-8-dir-123"), false},
		{agy.Adapter{Bin: "/opt/jetski/jetski"}, argv("jetski", "bees-8-dir-123"), true},
		{agy.Adapter{Bin: "/opt/jetski/jetski"}, argv("agy", "bees-8-dir-123"), false},
		{agy.Adapter{}, []string{"agy", "--add-dir"}, false},
		{agy.Adapter{}, nil, false},
	} {
		if got := c.a.Owns(c.argv, "bees-8"); got != c.want {
			t.Errorf("%+v Owns(%q) = %v, want %v", c.a, c.argv, got, c.want)
		}
	}
}

// agy has no ring yet: the message waits for the session's next bs inbox.
func TestRingHasNone(t *testing.T) {
	if err := (agy.Adapter{}).Ring(port.Target{Name: "bees-8", PID: 1}, "hi", port.Now); !errors.Is(err, port.ErrNoRing) {
		t.Errorf("ring: %v", err)
	}
}
