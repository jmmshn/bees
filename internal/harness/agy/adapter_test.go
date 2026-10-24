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
	"github.com/jmmshn/bees/internal/watch"
)

func worker() port.Start {
	return port.Start{Role: port.Worker, Name: "bees-8", Dir: "/wt/bees-8", PromptFile: "{{prompt.md}}", Prompt: "You are a worker.\n",
		Doorbell: "run: bs inbox", Deny: []string{"git push *main*", "it's *"}}
}

// A worker's launch adds its per-start dir, picks its custom agent, runs in accept-edits mode and
// gets the doorbell as its first prompt; the agent file lists the worker's tools and holds
// the prompt as its body (probe 1).
func TestPrepareAWorker(t *testing.T) {
	l, err := agy.Adapter{Model: "gemini-3.8-flash", Bs: "/bin/bs"}.Prepare(worker())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--add-dir", "{{dir}}", "--agent", "bs-worker", "--mode", "accept-edits", "--model=gemini-3.8-flash", "-i", "run: bs inbox"}
	if !reflect.DeepEqual(l.Args, want) {
		t.Errorf("args = %q, want %q", l.Args, want)
	}
	if len(l.Files) != 0 || len(l.Dir) != 2 {
		t.Errorf("files %v, dir %v: want only the agent file and hooks.json in the dir", l.Files, l.Dir)
	}
	// bees-13.13.13: agy says "not signed in" with any of these set (probed on agy.Probed).
	if want := []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"}; !reflect.DeepEqual(l.Unset, want) {
		t.Errorf("unset = %q, want %q", l.Unset, want)
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
	if want := []string{"--add-dir", "{{dir}}", "--agent", "bs-queen", "--mode", "accept-edits"}; !reflect.DeepEqual(l.Args, want) {
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

// A process is a session's own when it adds that session's per-start dir,
// <name>-dir-<digits>: never another session's whose name extends it. Which program runs
// it is the core's to check, against the spawn's record (bees-14.7).
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
		{agy.Adapter{Bin: "/opt/jetski/jetski"}, argv("/opt/jetski/jetski", "bees-8-dir-123"), true},
		{agy.Adapter{}, []string{"agy", "--add-dir"}, false},
		{agy.Adapter{}, nil, false},
	} {
		if got := c.a.Owns(c.argv, "bees-8"); got != c.want {
			t.Errorf("%+v Owns(%q) = %v, want %v", c.a, c.argv, got, c.want)
		}
	}
}

// Observe reads the conversation agy hands its shells, the last setting winning, and
// nothing else: the language server's token stays out of the record until a ring needs
// it. A shell agy didn't start tells nothing.
func TestObserveRecordsOnlyTheConversation(t *testing.T) {
	env := []string{"ANTIGRAVITY_CONVERSATION_ID=old", "ANTIGRAVITY_LS_ADDRESS=127.0.0.1:4100", "ANTIGRAVITY_CSRF_TOKEN=s3cret", "ANTIGRAVITY_CONVERSATION_ID=7d33c4b2"}
	if got := (agy.Adapter{}).Observe(env); !reflect.DeepEqual(got, map[string]string{agy.Conversation: "7d33c4b2"}) {
		t.Errorf("Observe = %v", got)
	}
	for _, env := range [][]string{nil, {"ANTIGRAVITY_CONVERSATION_ID="}, {"HOME=/h"}} {
		if got := (agy.Adapter{}).Observe(env); got != nil {
			t.Errorf("Observe(%q) = %v, want nothing", env, got)
		}
	}
}

// agy's ring is the session's mail watcher (bees-13.13.10): with one armed, Ring says it
// wakes the session and types nothing; with none, the message is held until its next turn.
func TestRingIsTheArmedWatcher(t *testing.T) {
	a := agy.Adapter{Cache: t.TempDir()}
	tg := port.Target{Name: "bees-8", PID: 1}
	if err := a.Ring(tg, "hi", port.Now); !errors.Is(err, port.ErrNoRing) || !strings.Contains(err.Error(), "next turn") {
		t.Errorf("no watcher: %v", err)
	}
	release, _, err := watch.Arm(watch.Path(a.Cache, "bees-8"), 4242)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := a.Ring(tg, "hi", port.Now); !errors.Is(err, port.ErrArmed) || !strings.Contains(err.Error(), "pid 4242") {
		t.Errorf("an armed watcher: %v", err)
	}
	if err := a.Ring(port.Target{Name: "bees-9", PID: 1}, "hi", port.Now); !errors.Is(err, port.ErrNoRing) {
		t.Errorf("another session's watcher rang bees-9: %v", err)
	}
}

// hooks.json runs bs hook --harness agy before each model call and when a turn stops,
// from the worker's workspace (agy runs it in the hooks.json dir, where bs would find no
// project): flat handler lists, as PreInvocation and Stop take (probed on agy.Probed).
func TestHooksRunBsHookForMail(t *testing.T) {
	bin := t.TempDir()
	bs := filepath.Join(bin, "my bs")
	if err := os.WriteFile(bs, []byte("#!/bin/sh\npwd\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	st := worker()
	st.Dir = t.TempDir()
	l, err := agy.Adapter{Bs: bs}.Prepare(st)
	if err != nil {
		t.Fatal(err)
	}
	var h map[string]map[string][]struct{ Type, Command string }
	if err := json.Unmarshal([]byte(l.Dir[".agents/hooks.json"]), &h); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []string{"PreInvocation", "Stop"} {
		hs := h["bs-mail"][ev]
		if len(hs) != 1 || hs[0].Type != "command" {
			t.Fatalf("%s: hooks.json = %s", ev, l.Dir[".agents/hooks.json"])
		}
		c := exec.Command("sh", "-c", hs[0].Command)
		c.Dir = bin
		out, err := c.Output()
		if want := st.Dir + "\nhook\n--harness\nagy\n" + ev + "\n"; err != nil || string(out) != want {
			t.Errorf("%s: the hook ran with %q (%v), want %q", ev, out, err, want)
		}
	}
}

// PreInvocation's payload as agy sends it (probed on agy.Probed): invocationNum 0 is a
// turn's first model call, sent even though protojson may drop a zero.
func TestFirstCall(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{`{"conversationId":"bc84","initialNumSteps":1,"invocationNum":0,"modelName":"gemini-3.8-flash-low","workspacePaths":["/k","/r"]}`, true},
		{`{"conversationId":"bc84","initialNumSteps":3,"invocationNum":1}`, false},
		{`{"conversationId":"bc84"}`, true},
	} {
		if got, err := agy.FirstCall(strings.NewReader(c.in)); err != nil || got != c.want {
			t.Errorf("FirstCall(%s) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	if _, err := agy.FirstCall(strings.NewReader("nope")); err == nil {
		t.Error("an unreadable payload read")
	}
}

// Stop's payload (probed): only a turn that ended on its own (NO_TOOL_CALL) is held; the
// keeper's cancel and agy's own limits are let stop.
func TestStopped(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{`{"conversationId":"bc84","error":"","executionNum":0,"fullyIdle":true,"terminationReason":"NO_TOOL_CALL"}`, true},
		{`{"terminationReason":"USER_CANCELED"}`, false},
		{`{"terminationReason":"MAX_FORCED_INVOCATIONS"}`, false},
	} {
		if got, err := agy.Stopped(strings.NewReader(c.in)); err != nil || got != c.want {
			t.Errorf("Stopped(%s) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
}

// The answers' shapes are agy's: PreInvocation's injectSteps with a plain-string
// userMessage, Stop's decision continue with its reason.
func TestHookAnswers(t *testing.T) {
	var b strings.Builder
	if err := agy.Inject(&b, "arm it"); err != nil || strings.TrimSpace(b.String()) != `{"injectSteps":[{"userMessage":"arm it"}]}` {
		t.Errorf("Inject = %q, %v", b.String(), err)
	}
	b.Reset()
	if err := agy.Hold(&b, "1 unread"); err != nil || strings.TrimSpace(b.String()) != `{"decision":"continue","reason":"1 unread"}` {
		t.Errorf("Hold = %q, %v", b.String(), err)
	}
}

// The program a launch runs is agy.bin, any base name (bees-14.7), and agy by default.
func TestProgram(t *testing.T) {
	if got := (agy.Adapter{}).Program(); got != "agy" {
		t.Errorf("default: %q", got)
	}
	if got := (agy.Adapter{Bin: "/opt/jetski/jetski"}).Program(); got != "/opt/jetski/jetski" {
		t.Errorf("bin: %q", got)
	}
}
