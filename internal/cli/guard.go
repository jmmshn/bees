package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/guards"
)

// agyShell is agy's shell tool: its args carry the command line as CommandLine.
const agyShell = "run_command"

// guard is agy's PreToolUse hook (bees-13.13.3): the hooks.json bs renders runs bs guard
// <glob>... before a tool call, with the call on stdin, and guards.Decide judges it by the
// worker's deny globs. It reads no config or logbook: everything it needs is in its
// arguments. It always exits 0, its answer on stdout: a deny as agy's JSON, and no decision
// as empty stdout, never {}, which agy reads as a deny (bees-13.80 probe 4). A call it can't
// read is denied: a guard fails closed.
func guard(args []string, stdin io.Reader, stdout io.Writer) int {
	call, err := agyCall(stdin)
	d := guards.Decision{Verdict: guards.Deny}
	if err != nil {
		d.Reason = err.Error()
	} else {
		d = guards.Decide(call, guards.Worker{Deny: args})
	}
	switch d.Verdict {
	case guards.None:
		return 0
	case guards.Deny:
	default: // allow isn't rendered until bees-13.13.9 wires it: deny it meanwhile
		d.Reason = "allow is not wired yet (bees-13.13.9)"
	}
	if err := json.NewEncoder(stdout).Encode(map[string]string{"decision": "deny", "reason": bsname.Cmd + " guard: " + d.Reason}); err != nil {
		return 1
	}
	return 0
}

// agyCall reads agy's PreToolUse payload: {"toolCall": {"name": ..., "args": {...}}}. A
// payload with no tool name, or a shell call with no command line, is an error: agy's
// payload changed shape.
func agyCall(stdin io.Reader) (guards.Call, error) {
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
	case tc.Name == agyShell && tc.Args.CommandLine == "":
		return guards.Call{}, errors.New("the " + agyShell + " call has no toolCall.args.CommandLine to check")
	case tc.Name == agyShell:
		return guards.Call{Tool: tc.Name, Command: tc.Args.CommandLine}, nil
	}
	return guards.Call{Tool: tc.Name}, nil
}
