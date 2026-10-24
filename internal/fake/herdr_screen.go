package fake

import (
	"strings"

	"github.com/jmmshn/bees/internal/herdr"
)

// Screen is what an agy agent in a pane shows, as agy's ring reads it (bees-13.97): the
// status herdr reports, the text in its input box, and whether its permission menu is
// up. herdr's blocked status lags the menu (probed in bees-13.95), so a test can put the
// menu up with any status.
type Screen struct {
	Status string // idle, done, working, blocked; "" is idle
	Box    string
	Menu   bool
}

// SetScreen sets what the agent in pane on session shows; a pane with a screen set has an
// agent for agent get and agent prompt, with no process behind it.
func (h *Herdr) SetScreen(session, pane string, sc Screen) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, p := h.server(session).pane(pane); p != nil {
		p.screen = &sc
	}
}

// Prompts is what agent prompt submitted to pane on session, oldest first: a menu it
// answered shows as "answered the menu: <text>".
func (h *Herdr) Prompts(session, pane string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, p := h.server(session).pane(pane); p != nil {
		return append([]string(nil), p.prompts...)
	}
	return nil
}

// agentScreen is pane id's agent's screen, nil when the pane has no agent herdr knows.
func agentScreen(s *server, id string) *Screen {
	_, p := s.pane(id)
	switch {
	case p == nil:
		return nil
	case p.screen != nil:
		return p.screen
	case p.agent != nil && p.agent.alive() && p.agent.name != "":
		return &Screen{}
	}
	return nil
}

func status(sc *Screen) string {
	if sc.Status == "" {
		return herdr.Idle
	}
	return sc.Status
}

// agentGet is herdr agent get TARGET: the agent's status (probed, 0.9.5), agent_not_found
// for a pane with none.
func (h *Herdr) agentGet(s *server, _ string, f flags) ([]byte, error) {
	id := first(f.pos)
	sc := agentScreen(s, id)
	if sc == nil {
		return nil, refusal("agent:get", "agent_not_found", "agent target %s not found", id)
	}
	return result("agent:get", map[string]any{"type": "agent_info", "agent": map[string]any{"pane_id": id, "agent": "agy", "agent_status": status(sc)}})
}

// agentPrompt is herdr agent prompt TARGET TEXT: refused as agent_blocked, typing
// nothing, only when herdr reports the agent blocked (0.9.5 --help). Otherwise it types
// TEXT after whatever the box holds and presses Enter, answering ok: a menu up that herdr
// doesn't report yet takes the Enter as its default, Yes, and TEXT is lost (bees-13.95).
func (h *Herdr) agentPrompt(s *server, _ string, f flags) ([]byte, error) {
	if len(f.pos) < 2 {
		return nil, usage("error: the following required arguments were not provided: <TARGET> <TEXT>")
	}
	id, text := f.pos[0], f.pos[1]
	sc := agentScreen(s, id)
	switch {
	case sc == nil:
		return nil, refusal("agent:prompt", "agent_not_found", "agent target %s not found", id)
	case status(sc) == herdr.Blocked:
		return nil, refusal("agent:prompt", "agent_blocked", "agent %s is blocked", id)
	}
	_, p := s.pane(id)
	if sc.Menu {
		p.prompts = append(p.prompts, "answered the menu: "+text)
		sc.Menu = false
	} else {
		p.prompts = append(p.prompts, sc.Box+text)
		sc.Box = ""
	}
	return result("agent:prompt", map[string]any{"type": "agent_prompted", "agent": map[string]any{"pane_id": id, "agent_status": herdr.Working}})
}

// paneRead is herdr pane read PANE: its screen as plain text (probed, 0.9.5), here agy
// 1.4.6's layout as bees-13.95 read it: the last turn over a short rule, then the input
// box between two full-width rules and a footer, or the permission menu in their place.
func (h *Herdr) paneRead(s *server, _ string, f flags) ([]byte, error) {
	id := first(f.pos)
	_, p := s.pane(id)
	if p == nil {
		return nil, refusal("pane:read", "pane_not_found", "pane %s not found", id)
	}
	sc := agentScreen(s, id)
	if sc == nil {
		return []byte("$ \n"), nil
	}
	short, full := strings.Repeat("─", 60), strings.Repeat("─", 119)
	ls := []string{short, "> Run bs inbox", "", "● Ran (bs inbox) (ctrl+o to expand)", ""}
	if sc.Menu {
		ls = append(ls, "Command", full, "", "Requesting permission for:", "   sleep 1", "", "Run this command?",
			"> 1. Yes, run command", "  2. Yes, and always allow in this conversation for commands that start with 'sleep'",
			"  4. No, cancel", "", "  ↑/↓ Navigate · tab Amend", "esc to cancel                    accept-edits · bs-worker")
	} else {
		box := strings.Split("> "+sc.Box, "\n")
		ls = append(append(append(ls, full), box...), full, "? for shortcuts                  accept-edits · bs-worker")
	}
	return []byte(strings.Join(ls, "\n") + "\n"), nil
}
