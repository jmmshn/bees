package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/prompt"
)

// Name is this harness's config name.
const Name = "claude_code"

// Adapter is the claude_code harness (ADR-0009), built per role from its config table.
type Adapter struct{ cfg config.ClaudeCode }

// New builds the adapter from one role's [<role>.claude_code] table.
func New(cfg config.ClaudeCode) Adapter { return Adapter{cfg} }

var _ harness.Harness = Adapter{}

// Kind is herdr's --kind for Claude, and the binary bs queen execs.
func (Adapter) Kind() string { return "claude" }

// Prepare builds the claude argv. A worker's starts with its doorbell as the first prompt
// and its settings in a --settings file; the queen's env is bs queen's exec env.
func (a Adapter) Prepare(st harness.Start) (harness.Launch, error) {
	var l harness.Launch
	l.Args = []string{"--name", st.Name}
	if st.Role == prompt.Worker {
		settings, err := workerSettings(st)
		if err != nil {
			return l, err
		}
		l.Files = map[string]string{"settings.json": settings}
		l.Args = append(l.Args, "--settings", "{{settings.json}}")
	}
	l.Args = append(l.Args, "--append-system-prompt-file", st.PromptFile)
	if st.Doorbell != "" {
		l.Args = append(l.Args, st.Doorbell)
	}
	if a.cfg.Model != "" {
		// On the line, not in --settings: --model beats ANTHROPIC_MODEL and settings files.
		// One argument, so a value starting with - can't become a flag of its own.
		if st.Role == prompt.Queen {
			l.Args = append(l.Args, "--model="+a.cfg.Model)
		} else {
			l.Args = append([]string{"--model=" + a.cfg.Model}, l.Args...)
		}
	}
	if st.Continue {
		l.Args = append([]string{"--continue"}, l.Args...)
	}
	return l, nil
}

// Ring rings session t at the pid the core recorded: see AtPID.
func (Adapter) Ring(t harness.Target, text string, p harness.Priority) error {
	s, err := AtPID(t.PID, t.Name)
	if err != nil {
		return err
	}
	return Ring(s, text, p)
}

// workerSettings is passed with --settings, as a private file in bs's cache made fresh at
// each start (inline JSON is too long to type into a pane: bees-8.3). It stays while the
// worker runs, since Claude may read its settings files again after startup. Claude reads
// settings.local.json from the main checkout even in a worktree, so per-worker settings
// can't live in the repo, and the auto mode classifier reads autoMode only from user,
// managed or --settings settings. It pins no env: the worker's bs finds its project from
// its herdr pane (ADR-0009 amendment 1).
//
// Every autoMode list keeps "$defaults": a list without it replaces the built-in rules
// (force push, exfiltration). The deny rules run before the classifier; a rule's * matches
// any text, a space or a slash too (probed on Claude Code 2.1.302), as guards.Rules.Deny
// expects.
func workerSettings(st harness.Start) (string, error) {
	deny := make([]string, len(st.Guards.Deny))
	for i, d := range st.Guards.Deny {
		deny[i] = "Bash(" + d + ")"
	}
	s := map[string]any{
		"crossSessionInbound": "accept",
		"autoMode": map[string][]string{
			"environment": append([]string{"$defaults"}, st.Guards.Environment...),
			"allow":       append([]string{"$defaults"}, st.Guards.Allow...),
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
// pane's PATH at each call (shape C of bees-8.19's probe, Claude Code 2.1.306). Hooks
// themselves keep the pane's PATH. A dir with a quote, $, backslash, backtick, colon or
// newline is refused: it would break out of one of the two quotings, or split PATH.
func pathHook(dirs []string) (map[string]string, error) {
	for _, d := range dirs {
		if strings.ContainsAny(d, "'\"$\\`:\n") {
			return nil, fmt.Errorf("%q: %w: claude_code can't quote a quote, $, backslash, backtick, colon or newline in it", d, harness.ErrBadPath)
		}
	}
	export := `export PATH="` + strings.Join(dirs, ":") + `:$PATH"`
	return map[string]string{"type": "command", "command": `echo '` + export + `' >> "$CLAUDE_ENV_FILE"`}, nil
}
