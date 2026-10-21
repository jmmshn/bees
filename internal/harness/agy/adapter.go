// Package agy is the harness for Antigravity's CLI, agy (ADR-0009, bees-12.12).
//
// agy has no flag for a system-prompt file or a settings file: it finds hooks, custom
// agents and rules only at fixed names under a .agents/ directory it discovers, or in the
// keeper's global ~/.gemini/config. Prepare renders what bs would put there, the guard
// hook and a custom agent that names the role prompt as an always-on rule, but bs has no
// proven place to write them that agy reads (the --add-dir probe in bees-12.12's notes).
// So Prepare refuses every start until one is proven: an agy worker never starts
// unguarded.
package agy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/prompt"
)

// Name is this harness's config name.
const Name = "agy"

// ErrUnguarded is Prepare's first refusal while bs has no proven place to load its guard
// hook and role prompt from: it blocks every start, so no later check hides it.
var ErrUnguarded = errors.New(`harness agy is staged: bs won't start agy sessions until it has a proven place to load its guard hook and role prompt from (bees-12.12's probes); to start sessions now, set harness = "claude_code" in the global config`)

// Adapter is the agy harness, built per role from its config table.
type Adapter struct {
	cfg     config.Agy
	trusted func(dir string) (bool, error) // nil: Trusted
	guarded bool                           // a home for the guard hook is proven; false until bees-12.12's probes land
}

// New builds the adapter from one role's [<role>.agy] table.
func New(cfg config.Agy) Adapter { return Adapter{cfg: cfg} }

var _ harness.Harness = Adapter{}

// Kind is herdr's --kind for agy, and its binary.
func (Adapter) Kind() string { return "agy" }

// Prepare builds the agy argv: the model, the role's custom agent and the doorbell as the
// first prompt (-i). Never --dangerously-skip-permissions. Files holds the custom agent
// and, for a worker, the hooks.json that runs bs guard before each command.
//
// Until a home for those files is proven it refuses every start with ErrUnguarded, before
// any other check. Then it refuses a resume: the conversation id agy gives a session is known only after it
// starts, and -c falls back to another conversation instead of saying there is none. It
// refuses a dir agy hasn't trusted, since agy loads hooks only in a folder trusted at
// startup; bs reads agy's trusted list and never writes it.
func (a Adapter) Prepare(st harness.Start) (harness.Launch, error) {
	var l harness.Launch
	if !a.guarded {
		return l, ErrUnguarded
	}
	if st.Continue {
		return l, fmt.Errorf("agy: %w (bees-12.12); bs end the worker and spawn it again to start fresh", harness.ErrNoResume)
	}
	if len(st.Path) > 0 {
		return l, fmt.Errorf("agy: bs can't put %s first on agy's commands' PATH yet (no per-session way is proven: bees-12.12); drop bs spawn --path, and worker.path from the config", strings.Join(st.Path, ", "))
	}
	trusted := a.trusted
	if trusted == nil {
		trusted = Trusted
	}
	ok, err := trusted(st.Dir)
	if err != nil {
		return l, err
	}
	if !ok {
		return l, fmt.Errorf("agy hasn't trusted %s, and it loads bs's guard hook only in a folder trusted at startup: start agy once in that folder, answer Yes to its trust prompt, quit it, then try again (a worktree bs hasn't made yet can't be trusted first: bees-12.12)", st.Dir)
	}
	agent := "bs-" + string(st.Role)
	l.Files = map[string]string{"agent.md": agentFile(agent, st.PromptFile)}
	if st.Role == prompt.Worker {
		l.Files["hooks.json"] = hooks(st.Guards.Deny)
	}
	if a.cfg.Model != "" {
		l.Args = append(l.Args, "--model="+a.cfg.Model) // one argument, so a value starting with - stays a value
	}
	l.Args = append(l.Args, "--agent", agent)
	if st.Doorbell != "" {
		l.Args = append(l.Args, "-i", st.Doorbell)
	}
	return l, nil
}

// Ring has no way in yet: agy agentapi send-message needs the session's language-server
// address and CSRF token, and exits 0 on error (bees-12.7). The message waits for the
// session's next bs inbox.
func (Adapter) Ring(harness.Target, string, harness.Priority) error { return harness.ErrNoRing }

// agentFile is a custom agent whose one rule is the role prompt, so it always applies.
func agentFile(name, promptFile string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: a bees %s, started by bs\nrules:\n  - %s\n---\n", name, strings.TrimPrefix(name, "bs-"), quoteYAML(promptFile))
}

// quoteYAML is s as a double-quoted YAML scalar; JSON's string syntax is a subset of it.
func quoteYAML(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// hooks is agy's hooks.json: before each run_command, bs guard gets the tool call on stdin
// and the deny globs as arguments, and answers deny or ask. The command runs under sh -c,
// so each glob is single-quoted.
func hooks(deny []string) string {
	cmd := []string{"bs", "guard"}
	for _, g := range deny {
		cmd = append(cmd, "'"+strings.ReplaceAll(g, "'", `'\''`)+"'")
	}
	b, _ := json.Marshal(map[string]any{
		"bs-guards": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "run_command",
				"hooks":   []any{map[string]any{"type": "command", "command": strings.Join(cmd, " ")}},
			}},
		},
	})
	return string(b)
}

// TrustFile is where agy 1.4.4 keeps the folders it trusts, under trustedWorkspaces.
func TrustFile() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), err
}

// Trusted reports whether dir is on agy's trusted list. Only an exact entry counts: that
// agy extends trust to a listed folder's subfolders is unprobed, and assuming it would start
// a worker whose hooks never load. A missing file trusts nothing; an unreadable one is an
// error, never "untrusted" passed off as an answer.
func Trusted(dir string) (bool, error) {
	path, err := TrustFile()
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var s struct {
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return false, fmt.Errorf("reading agy's trusted folders: %s: %w", path, err)
	}
	return filepath.IsAbs(dir) && slices.ContainsFunc(s.TrustedWorkspaces, func(t string) bool {
		return filepath.Clean(t) == filepath.Clean(dir)
	}), nil
}
