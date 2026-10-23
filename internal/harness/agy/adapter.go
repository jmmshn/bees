// Package agy is the harness for Antigravity's CLI, agy (bees-13.13, the parity table in
// bees-13.80's DESIGN).
//
// agy has no flag for a system-prompt or settings file: it finds custom agents and hooks
// under a .agents/ directory it discovers, which --add-dir adds (probe 1). So Prepare puts
// both in the per-start directory the core writes: the role's custom agent, its prompt the
// agent's body, and for a worker the hooks.json that runs bs guard before each command.
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

// Conversation is the Record key of the conversation a resume continues (bees-13.13.6
// records it from the worker's first bs call: agy names it only after the start).
const Conversation = "conversation"

// Adapter is the agy harness for one role: its [<role>.agy] model, the agy binary (""
// is agy on PATH), the bs its hook runs, an absolute path, and agy's settings.json, which
// holds its trusted folders ("" is ~/.gemini/antigravity-cli/settings.json).
type Adapter struct {
	Model    string
	Bin      string
	Bs       string
	Settings string
}

var _ port.Harness = Adapter{}

// Kind is herdr's --kind for agy, and the program bs queen execs.
func (Adapter) Kind() string { return "agy" }

// Owns reports whether argv is a launch Prepare built for session name: the configured
// binary, adding name's per-start dir. agy has no --name, but the core makes that dir as
// <name>-dir-<digits>, so its base names the session.
func (a Adapter) Owns(argv []string, name string) bool {
	if len(argv) == 0 || filepath.Base(argv[0]) != filepath.Base(a.bin()) {
		return false
	}
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
// the doorbell as the first prompt (-i). Never --dangerously-skip-permissions: it skips
// the hooks too. A resume continues the recorded conversation and is refused without one,
// never -c, which falls back to another conversation. A worker's PATH dirs are refused:
// agy has no per-session way to set them yet (bees-13.80 row 5).
func (a Adapter) Prepare(st port.Start) (port.Launch, error) {
	var l port.Launch
	switch {
	case st.Name == "":
		return l, errors.New("agy: no session name")
	case len(st.Path) > 0:
		return l, fmt.Errorf("agy: bs can't put %s first on agy's commands' PATH yet (bees-13.80 row 5); drop bs spawn --path, and worker.path from the config", strings.Join(st.Path, ", "))
	case st.Continue && st.Record[Conversation] == "":
		return l, errors.New("agy: no recorded conversation to resume (bees-13.13.6); bs end the worker and spawn it again to start fresh")
	case st.Role == port.Worker && !filepath.IsAbs(a.Bs):
		return l, fmt.Errorf("agy: the bs its guard hook runs must be an absolute path, not %q", a.Bs)
	}
	agent := "bs-" + string(st.Role)
	l.Dir = map[string]string{".agents/agents/" + agent + ".md": agentFile(agent, st.Role, st.Prompt)}
	if st.Role == port.Worker {
		l.Dir[".agents/hooks.json"] = hooks(a.Bs, st.Deny)
	}
	l.Args = []string{"--add-dir", "{{dir}}", "--agent", agent, "--mode", "auto"}
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

// Ring has no way in yet: agy agentapi send-message needs the session's language-server
// address and CSRF token (bees-13.80 row 7). The message waits for its next bs inbox.
func (Adapter) Ring(port.Target, string, port.Priority) error { return port.ErrNoRing }

// agentFile is the role's custom agent: its tools, and the role prompt as its body.
func agentFile(name string, role port.Role, prompt string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: a bees %s, started by %s\ntools: [%s]\n---\n%s", name, role, bsname.Cmd, strings.Join(Tools, ", "), prompt)
}

// hooks is agy's hooks.json: before each run_command, bs guard gets the tool call on
// stdin and the deny globs as arguments. agy runs the command under sh -c in the
// hooks.json dir, so bs is an absolute path and each word is single-quoted.
func hooks(bs string, deny []string) string {
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
	})
	return string(b)
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
