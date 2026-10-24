// Package agy is the harness for Antigravity's CLI, agy (bees-13.13, the parity table in
// bees-13.80's DESIGN).
//
// agy has no flag for a system-prompt or settings file: it finds custom agents and hooks
// under a .agents/ directory it discovers, which --add-dir adds (probe 1). So Prepare puts
// both in the per-start directory the core writes: the role's custom agent, its prompt the
// agent's body, and for a worker the hooks.json that runs bs guard before each command and
// bs hook when a turn stops, which holds the turn while mail is unread. Its ring types the
// doorbell into an idle session's empty input box (bees-13.97, ADR-0011).
package agy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/guards"
	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/port"
)

// Name is this harness's config name.
const Name = "agy"

// Shell is agy's shell tool: its args carry the command line as CommandLine.
const Shell = "run_command"

// Tools are the agent's tools. A custom agent without tools: frontmatter has none, and an
// unknown name fails the start (probe 1), so this is the list a worker needs and agy 1.4.6
// registers (command_status and send_command_input are in the binary but not the CLI's
// registry).
var Tools = []string{Shell, "view_file", "list_dir", "grep_search", "find_by_name", "write_to_file", "replace_file_content", "multi_replace_file_content"}

// Conversation is the Record key of the conversation a resume continues: agy names it
// only after the start, so the worker's first bs call observes it (bees-13.13.6).
const Conversation = "conversation"

// envConversation is the conversation id agy hands its run_command shells (bees-13.80).
// Its language server's address and CSRF token beside it are left alone until a ring
// needs them (bees-13.13.10): a token bs doesn't use, bs doesn't keep.
const envConversation = "ANTIGRAVITY_CONVERSATION_ID"

// Remote are the vars agy 1.4.6 reads as a remote session, saying "not signed in" when
// any is set and not empty (probed, bees-13.13.13): a herdr server started over ssh hands
// them to every pane. SSH_AUTH_SOCK doesn't count.
var Remote = []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"}

// Adapter is the agy harness for one role: its [<role>.agy] model, the agy binary (""
// is agy on PATH), the bs its hooks run, an absolute path, agy's settings.json, which
// holds its trusted folders ("" is ~/.gemini/antigravity-cli/settings.json), and the
// runner its ring drives herdr with (nil: no ring).
type Adapter struct {
	Model    string
	Bin      string
	Bs       string
	Settings string
	Run      port.Runner
}

var _ port.Harness = Adapter{}

// Kind is herdr's --kind for agy.
func (Adapter) Kind() string { return "agy" }

// Program is the configured binary, any base name (an agy-compatible build, bees-14.7):
// what a worker's launch runs and bs queen execs.
func (a Adapter) Program() string { return a.bin() }

// Owns reports whether argv is a launch Prepare built for session name: it adds name's
// per-start dir. agy has no --name, but the core makes that dir as <name>-dir-<digits>,
// so its base names the session. Which program runs it is the core's to check, against
// the spawn's record.
func (a Adapter) Owns(argv []string, name string) bool {
	i := slices.Index(argv, "--add-dir")
	if i < 0 || i+1 >= len(argv) {
		return false
	}
	n, ok := strings.CutPrefix(filepath.Base(argv[i+1]), name+"-dir-")
	return ok && n != "" && strings.Trim(n, "0123456789") == ""
}

func (a Adapter) bin() string {
	if a.Bin == "" {
		return "agy"
	}
	return a.Bin
}

// Prepare builds the agy argv: the per-start dir, the role's custom agent, the model and
// the doorbell as the first prompt (-i), with Remote unset. Never
// --dangerously-skip-permissions: it skips the hooks too. A resume continues the recorded
// conversation and is refused without one, never -c, which falls back to another
// conversation. A worker's PATH dirs are refused: agy has no per-session way to set them
// yet (bees-13.80 row 5).
func (a Adapter) Prepare(st port.Start) (port.Launch, error) {
	var l port.Launch
	switch {
	case st.Name == "":
		return l, errors.New("agy: no session name")
	case len(st.Path) > 0:
		return l, fmt.Errorf("agy: bs can't put %s first on agy's commands' PATH yet (bees-13.80 row 5); drop bs spawn --path, and worker.path from the config", strings.Join(st.Path, ", "))
	case st.Continue && st.Record[Conversation] == "":
		return l, errors.New("agy: no recorded conversation to resume: its session never ran a bs call, which records it; bs end the worker and spawn it again to start fresh")
	case st.Role == port.Worker && !filepath.IsAbs(a.Bs):
		return l, fmt.Errorf("agy: the bs its guard hook runs must be an absolute path, not %q", a.Bs)
	}
	agent := "bs-" + string(st.Role)
	l.Dir = map[string]string{".agents/agents/" + agent + ".md": agentFile(agent, st.Role, st.Prompt)}
	if st.Role == port.Worker {
		l.Dir[".agents/hooks.json"] = hooks(a.Bs, st.Dir, st.Deny)
	}
	l.Args = []string{"--add-dir", "{{dir}}", "--agent", agent, "--mode", Mode}
	l.Unset = Remote
	if a.Model != "" {
		l.Args = append(l.Args, "--model="+a.Model) // one argument, so a value starting with - stays a value
	}
	if st.Continue {
		l.Args = append(l.Args, "--conversation", st.Record[Conversation])
		l.Record = map[string]string{Conversation: st.Record[Conversation]}
	}
	if st.Doorbell != "" {
		l.Args = append(l.Args, "-i", st.Doorbell)
	}
	return l, nil
}

// ringTimeout bounds a ring's three herdr calls (motif 15).
const ringTimeout = 10 * time.Second

// Ring types text into session t's pane with herdr agent prompt, only while herdr reports
// it idle or done and its screen shows an empty input box and no permission menu: agy
// takes no push from outside yet (ADR-0011). It never types into a session that is
// working or blocked: herdr sees agy's permission menu 0.1-0.2 s late, and a ring in that
// gap answers the menu's default, Yes (bees-13.95). An idle agy shows no menu until a
// turn starts. Anything else is *port.Held, typing nothing: the mail waits in the logbook,
// a worker's Stop hook shows the doorbell if its turn ends on its own, and bs ring rings
// again (bees-13.97).
func (a Adapter) Ring(t port.Target, text string, _ port.Priority) error {
	if a.Run == nil {
		return fmt.Errorf("no herdr to type with: %w", port.ErrNoRing)
	}
	ctx, cancel := context.WithTimeout(context.Background(), ringTimeout)
	defer cancel()
	h := herdr.Client{Run: a.Run, Session: t.Session}
	busy := func(status string) error { return &port.Held{Why: t.Name + " is " + status} }
	switch st, err := h.AgentStatus(ctx, t.Pane); {
	case err != nil:
		return err
	case st != herdr.Idle && st != herdr.Done:
		return busy(st)
	}
	screen, err := h.Screen(ctx, t.Pane)
	if err != nil {
		return err
	}
	in, ok := Box(screen)
	switch {
	case Menu(screen):
		return busy("at a permission menu")
	case !ok:
		return &port.Held{Why: "no agy input box on " + t.Name + "'s screen (a view such as /tasks over it?)"}
	case !Ready(screen) && in == "":
		return &port.Held{Why: t.Name + "'s screen isn't at agy's prompt (no \"? for shortcuts\" under its input box)"}
	case in != "": // agy drops its footer while someone types; ready with text fails closed too
		return &port.Held{Why: t.Name + "'s input box has text (someone is typing)"}
	}
	if err := h.Prompt(ctx, t.Pane, text); errors.Is(err, herdr.ErrBlocked) {
		return busy(herdr.Blocked)
	} else if err != nil {
		return err
	}
	return nil
}

// Box is the text typed in agy's input box on screen, its visible lines in herdr's ansi
// format (agy 1.4.6-1.4.7, read in bees-13.95 and 13.97): a line starting with > between
// the screen's last two rules (lines of ─), and at most one footer line under them. Grey
// text is agy's placeholder hint (1.4.7: "Accept-edits mode: …" in an empty box), not
// typed, so it doesn't count. ok is false when the screen has no such box: a permission
// menu stands in its place, or the screen isn't agy's.
func Box(screen []string) (text string, ok bool) {
	screen = trim(screen)
	var rules []int
	for i, l := range screen {
		if rule(plain(l, true)) {
			rules = append(rules, i)
		}
	}
	if len(rules) < 2 {
		return "", false
	}
	top, bottom := rules[len(rules)-2], rules[len(rules)-1]
	in := screen[top+1 : bottom]
	if len(in) == 0 || !strings.HasPrefix(plain(in[0], true), ">") || len(screen)-bottom-1 != 1 {
		return "", false
	}
	typed := make([]string, len(in))
	for i, l := range in {
		typed[i] = plain(l, false)
	}
	typed[0] = strings.TrimPrefix(typed[0], ">")
	return strings.TrimSpace(strings.Join(typed, "\n")), true
}

// Ready reports whether agy shows its idle footer, "? for shortcuts", on the screen's
// last line: it says "esc to cancel" mid-turn and at a menu, and nothing there while
// someone types (agy 1.4.6-1.4.7).
func Ready(screen []string) bool {
	screen = trim(screen)
	return len(screen) > 0 && strings.HasPrefix(plain(screen[len(screen)-1], true), "? for shortcuts")
}

// Menu reports whether screen shows agy's permission menu where agy draws it, under the
// screen's last rule (agy 1.4.6-1.4.7: "Requesting permission for:" over numbered answers,
// the first "1. Yes, run command"): a "1. Yes" in the conversation above is the agent's
// own text.
func Menu(screen []string) bool {
	screen = trim(screen)
	last := -1
	for i, l := range screen {
		if rule(plain(l, true)) {
			last = i
		}
	}
	return slices.ContainsFunc(screen[last+1:], func(l string) bool {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(plain(l, true)), ">"))
		return strings.HasPrefix(l, "Requesting permission") || strings.HasPrefix(l, "1. Yes")
	})
}

// trim is screen without its trailing blank lines (rows under an inline UI's last line,
// reset-only escapes).
func trim(screen []string) []string {
	n := len(screen)
	for n > 0 && strings.TrimSpace(plain(screen[n-1], true)) == "" {
		n--
	}
	return screen[:n]
}

// plain is l with its escape sequences dropped and, unless grey, the text drawn grey or
// dim dropped too (SGR 2, 90, 38;5;8, or a truecolor grey 38;2;v;v;v).
func plain(l string, grey bool) string {
	var b strings.Builder
	dim := false
	for i := 0; i < len(l); i++ {
		if l[i] != 0x1b {
			if grey || !dim {
				b.WriteByte(l[i])
			}
			continue
		}
		if i+1 >= len(l) || l[i+1] != '[' {
			i++ // a two-byte escape
			continue
		}
		j := i + 2
		for j < len(l) && (l[j] < 0x40 || l[j] > 0x7e) {
			j++
		}
		if j < len(l) && l[j] == 'm' {
			dim = sgrDim(l[i+2:j], dim)
		}
		i = j
	}
	return strings.TrimRight(b.String(), " \r")
}

// greyRGB reports whether r, g, b is a mid or dark grey: equal, under 200.
func greyRGB(r, g, b string) bool {
	v, err := strconv.Atoi(r)
	return err == nil && r == g && g == b && v < 200
}

// sgrDim is whether text is grey or dim after the SGR parameters params, from dim before.
func sgrDim(params string, dim bool) bool {
	ps := strings.Split(params, ";")
	for k := 0; k < len(ps); k++ {
		switch p := ps[k]; {
		case p == "" || p == "0" || p == "22" || p == "39":
			dim = false
		case p == "2" || p == "90":
			dim = true
		case (p == "38" || p == "48") && k+1 < len(ps) && ps[k+1] == "5" && k+2 < len(ps):
			if p == "38" {
				dim = ps[k+2] == "8"
			}
			k += 2
		case (p == "38" || p == "48") && k+1 < len(ps) && ps[k+1] == "2":
			if p == "38" {
				dim = k+4 < len(ps) && greyRGB(ps[k+2], ps[k+3], ps[k+4])
			}
			k += 4
		case p >= "30" && p <= "37" && len(p) == 2, p >= "91" && p <= "97" && len(p) == 2:
			dim = false
		}
	}
	return dim
}

// rule reports whether l is one of agy's horizontal rules: 20 or more ─ and nothing else.
func rule(l string) bool {
	return len(l) >= 20*len("─") && strings.Trim(l, "─") == ""
}

// Observe reads the conversation agy runs a bs in from that bs's env: a resume continues
// it. An env without one (a shell agy didn't start) tells nothing.
func (Adapter) Observe(env []string) map[string]string {
	if cid := getenv(env, envConversation); cid != "" {
		return map[string]string{Conversation: cid}
	}
	return nil
}

// getenv is key's value in env, the last one set winning.
func getenv(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

// agentFile is the role's custom agent: its tools, and the role prompt as its body.
func agentFile(name string, role port.Role, prompt string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: a bees %s, started by %s\ntools: [%s]\n---\n%s", name, role, bsname.Cmd, strings.Join(Tools, ", "), prompt)
}

// hooks is agy's hooks.json: before each run_command, bs guard gets the tool call on
// stdin and the deny globs as arguments; when a turn stops, bs hook gets agy's payload, from the workspace dir so bs finds its project and caller.
// agy runs each command under sh -c in the hooks.json dir, so bs is an absolute path and
// each word is single-quoted.
func hooks(bs, dir string, deny []string) string {
	cmd := []string{quote(bs), "guard", "--harness", Name}
	for _, g := range deny {
		cmd = append(cmd, quote(g))
	}
	b, _ := json.Marshal(map[string]any{
		"bs-guards": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": Shell,
				"hooks":   []any{map[string]any{"type": "command", "command": strings.Join(cmd, " ")}},
			}},
		},
		"bs-mail": map[string]any{
			"Stop": []any{map[string]any{"type": "command", "command": mailHook(bs, dir, "Stop")}},
		},
	})
	return string(b)
}

// mailHook is the sh command that runs bs hook for event in dir.
func mailHook(bs, dir, event string) string {
	return "cd " + quote(dir) + " && " + quote(bs) + " hook --harness " + Name + " " + event
}

// quote is s as one sh word.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Call reads agy's PreToolUse payload: {"toolCall": {"name": ..., "args": {...}}}. A
// payload with no tool name, or a shell call with no command line, is an error: agy's
// payload changed shape.
func Call(stdin io.Reader) (guards.Call, error) {
	var in struct {
		ToolCall struct {
			Name string `json:"name"`
			Args struct {
				CommandLine string `json:"CommandLine"`
			} `json:"args"`
		} `json:"toolCall"`
	}
	if stdin == nil {
		return guards.Call{}, errors.New("no tool call on stdin")
	}
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return guards.Call{}, fmt.Errorf("can't read the tool call: %w", err)
	}
	tc := in.ToolCall
	switch {
	case tc.Name == "":
		return guards.Call{}, errors.New("the tool call has no toolCall.name")
	case tc.Name == Shell && tc.Args.CommandLine == "":
		return guards.Call{}, errors.New("the " + Shell + " call has no toolCall.args.CommandLine to check")
	case tc.Name == Shell:
		return guards.Call{Tool: tc.Name, Command: tc.Args.CommandLine}, nil
	}
	return guards.Call{Tool: tc.Name}, nil
}

// Answer writes d as agy's hook output: a deny as its JSON, and no decision as nothing,
// never {}, which agy reads as a deny (bees-13.80 probe 4). An allow isn't wired until
// bees-13.13.9, so it is denied meanwhile.
func Answer(w io.Writer, d guards.Decision) error {
	switch d.Verdict {
	case guards.None:
		return nil
	case guards.Deny:
	default:
		d.Reason = "allow is not wired yet (bees-13.13.9)"
	}
	return json.NewEncoder(w).Encode(map[string]string{"decision": "deny", "reason": bsname.Cmd + " guard: " + d.Reason})
}

// Stopped reads Stop's payload: whether the turn ended on its own (NO_TOOL_CALL), not
// cancelled by the keeper or cut by one of agy's limits, which a hook doesn't fight.
func Stopped(stdin io.Reader) (bool, error) {
	var in struct {
		TerminationReason string `json:"terminationReason"`
	}
	if err := decode(stdin, &in); err != nil {
		return false, err
	}
	return in.TerminationReason == "NO_TOOL_CALL", nil
}

// Hold writes Stop's answer keeping the turn going, reason its next message.
func Hold(w io.Writer, reason string) error {
	return json.NewEncoder(w).Encode(map[string]string{"decision": "continue", "reason": reason})
}

func decode(stdin io.Reader, v any) error {
	if stdin == nil {
		return errors.New("no hook payload on stdin")
	}
	if err := json.NewDecoder(stdin).Decode(v); err != nil {
		return fmt.Errorf("can't read the hook payload: %w", err)
	}
	return nil
}
