package herdr_test

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/jmmshn/bees/cmd/bsx/internal/herdr"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// AgentArgCases are arguments herdr's agent start was probed with (bees-1.14, 0.9.3): it
// refuses any Unicode control character and passes everything else through.
var agentArgCases = map[string]bool{
	"plain": true, "": true, "-x": true, `'quotes" $dollar` + "`tick`": true, "é—🐝": true,
	"line separator": true, `{"crossSessionInbound":"accept"}`: true,
	"new\nline": false, "carriage\rreturn": false, "tab\there": false, "esc\x1b": false,
	"del\x7f": false, "c1\u0085next": false, "c1\u009bcsi": false,
}

func TestCheckAgentArgs(t *testing.T) {
	for arg, ok := range agentArgCases {
		err := herdr.CheckAgentArgs([]string{"--name", "bees-8", arg})
		if (err == nil) != ok || (err != nil && !errors.Is(err, herdr.ErrUnsafeArg)) {
			t.Errorf("CheckAgentArgs(%q) = %v, want ok=%v", arg, err, ok)
		}
	}
	long := strings.Repeat("x", 1024)
	if err := herdr.CheckAgentArgs([]string{"--name", "bees-8", long}); !errors.Is(err, herdr.ErrLongLaunch) {
		t.Fatalf("1024 bytes: %v, want ErrLongLaunch", err)
	}
	if err := herdr.CheckAgentArgs([]string{"--name", "bees-8", long[:500]}); err != nil {
		t.Fatalf("500 bytes: %v", err)
	}
}

// TestCheckAgentArgsMatchesRealHerdr holds Unsafe, and so CheckAgentArgs and the fake
// herdr, to the installed herdr. herdr checks agent arguments before it looks up the
// pane, so a pane that doesn't exist makes this side-effect free. Skipped without a
// running herdr.
func TestCheckAgentArgsMatchesRealHerdr(t *testing.T) {
	if exec.Command("herdr", "workspace", "list").Run() != nil {
		t.Skip("no running herdr")
	}
	for arg, ok := range agentArgCases {
		out, _ := exec.Command("herdr", "agent", "start", "bs-contract", "--kind", "claude", "--pane", "bs-contract-no-such-pane", "--", arg).CombinedOutput()
		var he struct{ Error struct{ Code string } }
		if json.Unmarshal(out, &he) != nil {
			t.Fatalf("herdr output for %q: %s", arg, out)
		}
		if refused := he.Error.Code == "invalid_agent_argument"; refused == ok || (ok && he.Error.Code != "agent_pane_not_found") {
			t.Errorf("herdr on %q: %s; Unsafe says ok=%v", arg, he.Error.Code, ok)
		}
		if herdr.Unsafe(arg) == ok {
			t.Errorf("Unsafe(%q) = %v", arg, !ok)
		}
	}
}

// The client reads herdr's refusal envelope into an *Error that matches its sentinel,
// and a herdr that can't run is ErrDown.
func TestClientReadsHerdrsEnvelope(t *testing.T) {
	if exec.Command("herdr", "workspace", "list").Run() != nil {
		t.Skip("no running herdr")
	}
	c := herdr.Client{Run: port.Exec{}}
	_, err := c.ProcessInfo(context.Background(), "bs-contract-no-such-pane")
	var he *herdr.Error
	if !errors.As(err, &he) || !errors.Is(err, herdr.ErrPaneGone) {
		t.Fatalf("err = %#v", err)
	}
}
