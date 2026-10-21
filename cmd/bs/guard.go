package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jmmshn/bees/internal/guards"
)

// cmdGuard is agy's PreToolUse hook (bees-12.12): agy has no deny list of its own, so the
// hooks.json bs renders runs bs guard <glob>... before each run_command, with the tool
// call on stdin. A command line matching a glob is denied; anything else is asked, so
// agy's own permission checks still decide it. A call bs can't read, or one with no
// command line (agy's payload changed shape), is denied: a guard fails closed.
func cmdGuard(_ context.Context, args []string, stdin io.Reader, stdout, _ io.Writer) error {
	var in struct {
		ToolCall struct {
			Args struct {
				CommandLine string `json:"CommandLine"`
			} `json:"args"`
		} `json:"toolCall"`
	}
	out := map[string]string{"decision": "ask"}
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		out = map[string]string{"decision": "deny", "reason": fmt.Sprintf("bs guard: can't read the tool call: %v", err)}
	} else if in.ToolCall.Args.CommandLine == "" {
		out = map[string]string{"decision": "deny", "reason": "bs guard: the tool call has no toolCall.args.CommandLine to check"}
	} else if glob, ok := (guards.Rules{Deny: args}).Denies(in.ToolCall.Args.CommandLine); ok {
		out = map[string]string{"decision": "deny", "reason": fmt.Sprintf("bs guard: the keeper's guards deny %q", glob)}
	}
	return json.NewEncoder(stdout).Encode(out)
}
