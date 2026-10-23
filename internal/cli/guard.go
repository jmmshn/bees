package cli

import (
	"errors"
	"io"

	"github.com/jmmshn/bees/internal/guards"
	"github.com/jmmshn/bees/internal/harness/agy"
)

// hook is one harness's side of bs guard: how it hands a tool call over, and how it takes
// the answer back.
type hook struct {
	call   func(stdin io.Reader) (guards.Call, error)
	answer func(w io.Writer, d guards.Decision) error
}

// hooks are the harnesses bs guard serves, by config name.
var hooks = map[string]hook{agy.Name: {agy.Call, agy.Answer}}

// guard is a harness's PreToolUse hook (bees-13.13.3): the hook config bs renders runs bs
// guard --harness <name> <glob>... before a tool call, with the call on stdin, and
// guards.Decide judges it by the worker's deny globs; the harness's adapter reads the call
// and writes the answer. It reads no config or logbook: everything it needs is in its
// arguments. It always exits 0, its answer on stdout. A call it can't read is denied: a
// guard fails closed. With no harness it knows, it has no way to answer, so it answers as
// agy, the one harness whose hook reads a deny from stdout.
func guard(args []string, stdin io.Reader, stdout io.Writer) int {
	h, err := hooks[agy.Name], errors.New("no --harness <name> before the globs")
	if len(args) >= 2 && args[0] == "--harness" {
		var ok bool
		if h, ok = hooks[args[1]]; ok {
			err = nil
		} else {
			h, err = hooks[agy.Name], errors.New("no guard hook for harness "+args[1])
		}
		args = args[2:]
	}
	d := guards.Decision{Verdict: guards.Deny}
	var call guards.Call
	if err == nil {
		call, err = h.call(stdin)
	}
	if err != nil {
		d.Reason = err.Error()
	} else {
		d = guards.Decide(call, guards.Worker{Deny: args})
	}
	if h.answer(stdout, d) != nil {
		return 1
	}
	return 0
}
