// Package agy is the harness for Antigravity's CLI, agy (bees-13.13, the parity table in
// bees-13.80's DESIGN).
//
// agy has no flag for a system-prompt or settings file: it finds custom agents and hooks
// under a .agents/ directory it discovers, which --add-dir adds (probe 1). So Prepare puts
// both in the per-start directory the core writes: the role's custom agent, its prompt the
// agent's body, and for a worker the hooks.json that runs bs guard before each command and
// bs hook around each turn, which keeps a mail watcher armed: agy's ring (bees-13.13.10).
package agy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/guards"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/watch"
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
// is agy on PATH), the bs its hook runs, an absolute path, and agy's settings.json, which
// holds its trusted folders ("" is ~/.gemini/antigravity-cli/settings.json), and bs's
// cache dir, which holds each session's mail watcher lock.
type Adapter struct {
	Model    string
	Bin      string
	Bs       string
	Settings string
	Cache    string
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

// Ring types nothing: agy wakes an idle session when a background command it ran exits
// (probed on 1.4.6), so the session's ring is its own bs inbox --wait, which exits when
// mail comes, and its hooks keep one armed. Ring only says whether one is: ErrArmed, or
// ErrNoRing with the message held until the session's next turn, where its Stop hook
// holds it until it reads its mail.
func (a Adapter) Ring(t port.Target, _ string, _ port.Priority) error {
	pid, armed, err := watch.Armed(watch.Path(a.Cache, t.Name))
	switch {
	case err != nil:
		return fmt.Errorf("can't tell whether its watcher is armed: %w", err)
	case !armed:
		return fmt.Errorf("no %s inbox --wait armed, so it's held until its next turn: %w", bsname.Cmd, port.ErrNoRing)
	}
	return fmt.Errorf("%w (%s inbox --wait, pid %d)", port.ErrArmed, bsname.Cmd, pid)
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
// stdin and the deny globs as arguments; before each model call and when a turn stops,
// bs hook gets agy's payload, from the workspace dir so bs finds its project and caller.
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
			"PreInvocation": []any{map[string]any{"type": "command", "command": mailHook(bs, dir, "PreInvocation")}},
			"Stop":          []any{map[string]any{"type": "command", "command": mailHook(bs, dir, "Stop")}},
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

// Probed is the agy release bs's agy contracts were last probed on (bees-13.13.15):
// the hook payloads (PreToolUse toolCall.name and toolCall.args.CommandLine read by Call;
// PreInvocation invocationNum read by FirstCall; Stop terminationReason == NO_TOOL_CALL
// read by Stopped), the hook answers (Answer's decision deny and empty stdout for no
// decision; Inject's injectSteps; Hold's decision continue), and the --mode log lines
// Mode / Adapter.Took parses.
const Probed = "1.4.6"

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

// FirstCall reads PreInvocation's payload: whether this model call is its turn's first
// (invocationNum 0, which protojson may leave out).
func FirstCall(stdin io.Reader) (bool, error) {
	var in struct {
		InvocationNum int `json:"invocationNum"`
	}
	if err := decode(stdin, &in); err != nil {
		return false, err
	}
	return in.InvocationNum == 0, nil
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

// Inject writes PreInvocation's answer adding msg as a user message.
func Inject(w io.Writer, msg string) error {
	return json.NewEncoder(w).Encode(map[string]any{"injectSteps": []any{map[string]string{"userMessage": msg}}})
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
