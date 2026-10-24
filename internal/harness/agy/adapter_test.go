package agy_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/fake"
	"github.com/jmmshn/bees/internal/harness/agy"
	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/port"
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

// The queen's launch has her agent and the Stop mail hook, so mail stored mid-turn
// wakes her when it ends (bees-13.13.21), and no guard: guards are a worker's.
func TestPrepareTheQueen(t *testing.T) {
	l, err := agy.Adapter{Bs: "/bin/bs"}.Prepare(port.Start{Role: port.Queen, Name: "queen-bees", Dir: "/repo", Prompt: "You are the queen.\n"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--add-dir", "{{dir}}", "--agent", "bs-queen", "--mode", "accept-edits"}; !reflect.DeepEqual(l.Args, want) {
		t.Errorf("args = %q, want %q", l.Args, want)
	}
	if !strings.HasSuffix(l.Dir[".agents/agents/bs-queen.md"], "---\nYou are the queen.\n") {
		t.Errorf("dir = %v", l.Dir)
	}
	var h map[string]map[string][]struct{ Type, Command string }
	if err := json.Unmarshal([]byte(l.Dir[".agents/hooks.json"]), &h); err != nil {
		t.Fatal(err)
	}
	stop := h["bs-mail"]["Stop"]
	if _, ok := h["bs-guards"]; ok || len(h) != 1 || len(stop) != 1 || stop[0].Command != "cd '/repo' && '/bin/bs' hook --harness agy Stop" {
		t.Errorf("hooks.json = %s", l.Dir[".agents/hooks.json"])
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

// agy's ring types the doorbell only into an idle or done session with an empty input
// box and no menu on screen (bees-13.97). A working or blocked one, one whose box holds
// someone's text, and one showing a permission menu herdr doesn't report yet (the 0.1-0.2 s
// lag that let a ring answer Yes in bees-13.95) get nothing typed: *port.Held, saying why.
func TestRingTypesOnlyIntoAnIdleEmptyBox(t *testing.T) {
	h := &fake.Herdr{Home: t.TempDir()}
	ws, err := herdr.Client{Run: h, Session: "toy"}.CreateWorkspace(context.Background(), t.TempDir(), "bees-8")
	if err != nil {
		t.Fatal(err)
	}
	a := agy.Adapter{Run: h}
	tg := port.Target{Name: "bees-8", Session: "toy", Pane: ws.Pane, PID: 1}
	held := func(sc fake.Screen, why string) {
		t.Helper()
		h.SetScreen("toy", ws.Pane, sc)
		var hd *port.Held
		if err := a.Ring(tg, "[bs] 1 unread", port.Now); !errors.As(err, &hd) || !strings.Contains(hd.Why, why) {
			t.Errorf("%+v: Ring = %v, want held: %q", sc, err, why)
		}
		if p := h.Prompts("toy", ws.Pane); len(p) != 0 {
			t.Fatalf("%+v: typed %q", sc, p)
		}
	}
	held(fake.Screen{Status: herdr.Working}, "bees-8 is working")
	held(fake.Screen{Status: herdr.Blocked, Menu: true}, "bees-8 is blocked")
	held(fake.Screen{Status: herdr.Idle, Menu: true}, "bees-8 is at a permission menu")
	held(fake.Screen{Status: herdr.Working, Menu: true}, "bees-8 is working")
	held(fake.Screen{Status: herdr.Idle, Box: "keeper half typed"}, "bees-8's input box has text (someone is typing)")
	held(fake.Screen{Status: herdr.Idle, Box: "keeper half typed", Hint: "Accept-edits mode"}, "bees-8's input box has text")
	held(fake.Screen{Status: "unknown"}, "bees-8 is unknown")
	for i, st := range []string{herdr.Idle, herdr.Done} {
		h.SetScreen("toy", ws.Pane, fake.Screen{Status: st, Hint: "Accept-edits mode: file edits auto-approved", Grey: []string{"", "38;2;128;128;128"}[i]})
		if err := a.Ring(tg, "[bs] 1 unread", port.Now); err != nil {
			t.Fatalf("%s: Ring = %v", st, err)
		}
		if p := h.Prompts("toy", ws.Pane); len(p) != i+1 || p[i] != "[bs] 1 unread" {
			t.Fatalf("%s: typed %q", st, p)
		}
	}
	if err := (agy.Adapter{}).Ring(tg, "hi", port.Now); !errors.Is(err, port.ErrNoRing) {
		t.Errorf("no runner: %v", err)
	}
	if err := a.Ring(port.Target{Name: "bees-9", Session: "toy", Pane: "w9:p9"}, "hi", port.Now); !errors.Is(err, herdr.ErrPaneGone) {
		t.Errorf("a pane herdr doesn't know: %v", err)
	}
}

// Box reads agy's input box off screens bees-13.95 recorded (probed on agy.Probed): empty,
// holding text (on two lines), and none at all under a permission menu, whose answers sit
// between the same rules. Menu tells the menu apart.
func TestBoxAndMenuReadAgysScreen(t *testing.T) {
	short, full := strings.Repeat("─", 60), strings.Repeat("─", 119)
	footer := "? for shortcuts                                              accept-edits · bs-worker (Gemini 3.8 Flash · low)  /agents"
	idle := []string{short, "> Run bs inbox once, then end your turn.", "", "● Ran (bs inbox) (ctrl+o to expand)", "", "  Inbox checked.", "", full, ">", full, footer}
	typed := []string{short, "> hi", "", full, `> keeper half typed c6-01[bs] 1 unread (queen-lt) — run: bs inbox   (reply with: bs send`, `  queen-lt "...") #c6-01`, full, footer}
	g, b, r := "\x1b[0m\x1b[38;5;8m", "\x1b[0m\x1b[38;5;12m", "\x1b[0m"
	menu := []string{short, "> Run the shell command: sleep 1", "", "● Ran (sleep 1) (ctrl+o to expand)", "", "Command", full, "", "Requesting permission for:", "   sleep 1", "",
		"Run this command?", "> 1. Yes, run command", "  4. No, cancel", "", "  ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command",
		"esc to cancel                                                accept-edits · bs-worker (Gemini 3.8 Flash · low)  /agents"}
	for _, c := range []struct {
		name   string
		screen []string
		box    string
		ok     bool
		menu   bool
	}{
		{"idle", idle, "", true, false},
		{"typed", typed, "keeper half typed c6-01[bs] 1 unread (queen-lt) — run: bs inbox   (reply with: bs send\n  queen-lt \"...\") #c6-01", true, false},
		{"menu", menu, "", false, true},
		{"a shell", []string{"$ ls", "$"}, "", false, false},
		{"/tasks under the box", []string{short, full, ">", full, "Tasks", "  No background tasks.", "", "Keyboard: ↑/↓ Navigate  enter View output", "", "   accept-edits · bs-worker"}, "", false, false},
		// agy 1.4.7 in herdr's ansi format (bees-13.97): its grey placeholder in an empty box, and typed text
		{"hint", []string{short, g + full + r, b + ">" + r + " " + g + "Accept-edits mode: file edits auto-approved (shift+tab to cycle)" + r + "\r", g + full + r, g + footer + r}, "", true, false},
		{"truecolor hint", []string{short, g + full + r, b + ">" + r + " \x1b[38;2;128;128;128mAccept-edits mode" + r, g + full + r, g + footer + r}, "", true, false},
		{"blank rows under it", append(slices.Clone(idle), "", "\x1b[0m", "   "), "", true, false},
		{"the agent's own 1. Yes", append([]string{short, "  Which way?", "  1. Yes, once CI passes", "  2. No"}, idle...), "", true, false},
		{"typed ansi", []string{short, g + full + r, b + ">" + r + " half typed\r", g + full + r, g + footer + r}, "half typed", true, false},
	} {
		if box, ok := agy.Box(c.screen); box != c.box || ok != c.ok {
			t.Errorf("%s: Box = %q, %v; want %q, %v", c.name, box, ok, c.box, c.ok)
		}
		if got := agy.Menu(c.screen); got != c.menu {
			t.Errorf("%s: Menu = %v", c.name, got)
		}
	}
	for _, c := range []struct {
		footer string
		want   bool
	}{{footer, true}, {"\x1b[0m\x1b[38;5;8m" + footer, true}, {"      accept-edits · bs-worker", false}, {"esc to cancel      accept-edits · bs-worker", false}} {
		if got := agy.Ready([]string{full, ">", full, c.footer, ""}); got != c.want {
			t.Errorf("Ready with footer %q = %v", c.footer, got)
		}
	}
}

// hooks.json runs bs hook --harness agy when a turn stops, from the worker's workspace
// (agy runs it in the hooks.json dir, where bs would find no project): a flat handler
// list, as Stop takes (probed on agy.Probed). Nothing runs before a model call: the ring
// no longer asks the model to arm a watcher (bees-13.97).
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
	if len(h["bs-mail"]) != 1 {
		t.Fatalf("bs-mail hooks: %s", l.Dir[".agents/hooks.json"])
	}
	hs := h["bs-mail"]["Stop"]
	if len(hs) != 1 || hs[0].Type != "command" {
		t.Fatalf("hooks.json = %s", l.Dir[".agents/hooks.json"])
	}
	c := exec.Command("sh", "-c", hs[0].Command)
	c.Dir = bin
	out, err := c.Output()
	if want := st.Dir + "\nhook\n--harness\nagy\nStop\n"; err != nil || string(out) != want {
		t.Errorf("the hook ran with %q (%v), want %q", out, err, want)
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

// Stop's answer is agy's shape: decision continue with its reason.
func TestHookAnswers(t *testing.T) {
	var b strings.Builder
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
