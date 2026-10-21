package claude

import (
	"encoding/json"

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

// Ring rings the live session called t.Name: the newest in Claude's registry.
func (Adapter) Ring(t harness.Target, text string, p harness.Priority) error {
	s, err := Find(t.Name)
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
// managed or --settings settings. env is st.Env: the worker's bs finds its project from
// BS_PROJECT, the spawner's project, never from the worktree's git dir, which names
// another project in a --repo worktree (bees-1.54).
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
	b, err := json.Marshal(map[string]any{
		"crossSessionInbound": "accept",
		"env":                 st.Env,
		"autoMode": map[string][]string{
			"environment": append([]string{"$defaults"}, st.Guards.Environment...),
			"allow":       append([]string{"$defaults"}, st.Guards.Allow...),
		},
		"permissions": map[string][]string{"deny": deny},
	})
	return string(b), err
}
