package herdr

import (
	"context"
	"fmt"
	"strings"
)

// Agent statuses herdr reports (0.9.5): idle and done sit at the input box, working is
// mid-turn, blocked waits on an answer such as a permission menu.
const (
	Idle    = "idle"
	Done    = "done"
	Working = "working"
	Blocked = "blocked"
)

// AgentStatus is the status herdr reports for the agent in pane. A pane with no agent
// herdr knows is ErrPaneGone.
func (c Client) AgentStatus(ctx context.Context, pane string) (string, error) {
	var r struct {
		Result struct {
			Agent struct {
				Status string `json:"agent_status"`
			} `json:"agent"`
		} `json:"result"`
	}
	if err := c.call(ctx, &r, "agent", "get", pane); err != nil {
		return "", err
	}
	if r.Result.Agent.Status == "" {
		return "", fmt.Errorf("herdr agent get %s: no agent_status in its output", pane)
	}
	return r.Result.Agent.Status, nil
}

// Screen is pane's visible screen in herdr's ansi format, its colours kept (pane read
// prints text, not JSON: probed, 0.9.5), each line's trailing spaces and \r cut.
func (c Client) Screen(ctx context.Context, pane string) ([]string, error) {
	b, err := c.output(ctx, "pane", "read", pane, "--source", "visible", "--format", "ansi")
	if err != nil {
		return nil, err
	}
	ls := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	for i, l := range ls {
		ls[i] = strings.TrimRight(l, " \r")
	}
	return ls, nil
}

// Prompt types text into pane's agent and presses Enter. herdr refuses an agent it
// reports blocked (ErrBlocked) before typing, but types into one working or at a menu it
// hasn't seen yet, and its ok means the keys went in, never that they were read
// (bees-13.95). A text holding a control character is refused here.
func (c Client) Prompt(ctx context.Context, pane, text string) error {
	if Unsafe(text) {
		return fmt.Errorf("agent prompt %.40q: %w", text, ErrUnsafeArg)
	}
	return c.call(ctx, nil, "agent", "prompt", pane, text)
}
