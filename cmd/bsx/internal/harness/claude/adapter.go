// Package claude is the claude_code harness (ADR-0010's Harness port, carried from the
// pilot): it builds Claude Code's launch, finds a running session in Claude's session
// registry by the pid the core recorded, and rings it over its cross-session messaging
// socket.
package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// Name is this harness's config name.
const Name = "claude_code"

// ErrBadPath is a Start.Path dir the settings' PATH export can't quote.
var ErrBadPath = errors.New("a PATH dir claude_code can't quote")

// Adapter is the claude_code harness for one role: its [<role>.claude_code] model, and
// the home whose ~/.claude/sessions it reads ("" is the user's).
type Adapter struct {
	Model string
	Home  string
}

var _ port.Harness = Adapter{}

// Kind is herdr's --kind for Claude, and the program bs queen execs.
func (Adapter) Kind() string { return "claude" }

// Prepare builds the claude argv. A worker's starts with its doorbell as the first prompt
// and its settings in a --settings file (a per-start file the core writes); the queen's
// env is bs queen's exec env.
func (a Adapter) Prepare(st port.Start) (port.Launch, error) {
	var l port.Launch
	if st.Name == "" {
		return l, errors.New("claude_code: no session name")
	}
	l.Args = []string{"--name", st.Name}
	if st.Role == port.Worker {
		settings, err := workerSettings(st)
		if err != nil {
			return l, err
		}
		l.Files = map[string]string{"settings.json": settings}
		l.Args = append(l.Args, "--settings", "{{settings.json}}")
	}
	if st.PromptFile != "" {
		l.Args = append(l.Args, "--append-system-prompt-file", st.PromptFile)
	}
	if st.Doorbell != "" {
		l.Args = append(l.Args, st.Doorbell)
	}
	if a.Model != "" {
		// On the line, not in --settings: --model beats ANTHROPIC_MODEL and settings files.
		// One argument, so a value starting with - can't become a flag of its own.
		l.Args = append([]string{"--model=" + a.Model}, l.Args...)
	}
	if st.Continue {
		// Only for a conversation the session has had: claude --continue exits when there
		// is none, so the core sets it only once the worker read its inbox.
		l.Args = append([]string{"--continue"}, l.Args...)
	}
	return l, nil
}

// Ring rings session t at the pid the core recorded: see AtPID.
func (a Adapter) Ring(t port.Target, text string, p port.Priority) error {
	home := a.Home
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return err
		}
	}
	s, err := AtPID(home, t.PID, t.Name)
	if err != nil {
		return err
	}
	return Ring(s, text, p)
}

// workerSettings is passed with --settings, as a private per-start file (inline JSON is
// too long to type into a pane: bees-8.3). It stays while the worker runs, since Claude
// may read its settings files again after startup. Claude reads settings.local.json from
// the main checkout even in a worktree, so per-worker settings can't live in the repo, and
// the auto mode classifier reads autoMode only from user, managed or --settings settings.
// It pins no env: the worker's bs finds its project from its herdr pane (ADR-0010).
//
// Every autoMode list keeps "$defaults": a list without it replaces the built-in rules
// (force push, exfiltration). The deny rules run before the classifier; a rule's * matches
// any text, a space or a slash too (probed on Claude Code 2.1.302).
func workerSettings(st port.Start) (string, error) {
	deny := make([]string, len(st.Deny))
	for i, d := range st.Deny {
		deny[i] = "Bash(" + d + ")"
	}
	s := map[string]any{
		"crossSessionInbound": "accept",
		"autoMode": map[string][]string{
			"environment": {"$defaults"},
			"allow":       {"$defaults"},
		},
		"permissions": map[string][]string{"deny": deny},
	}
	if len(st.Path) > 0 {
		h, err := pathHook(st.Path)
		if err != nil {
			return "", err
		}
		s["hooks"] = map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{h}}}}
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// pathHook puts dirs first on the PATH of the worker's Bash tool. Not env.PATH: Claude
// sets that literally, without expanding $PATH, and bs can't know the pane's PATH. The
// Bash tool sources CLAUDE_ENV_FILE after the shell profile, so the export builds on the
// pane's PATH at each call (shape C of bees-8.19's probe, Claude Code 2.1.306). A dir with
// a quote, $, backslash, backtick, colon or newline is refused: it would break out of one
// of the two quotings, or split PATH.
func pathHook(dirs []string) (map[string]string, error) {
	for _, d := range dirs {
		if strings.ContainsAny(d, "'\"$\\`:\n") {
			return nil, fmt.Errorf("%q: %w: claude_code can't quote a quote, $, backslash, backtick, colon or newline in it", d, ErrBadPath)
		}
	}
	export := `export PATH="` + strings.Join(dirs, ":") + `:$PATH"`
	return map[string]string{"type": "command", "command": `echo '` + export + `' >> "$CLAUDE_ENV_FILE"`}, nil
}
