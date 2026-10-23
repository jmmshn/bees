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
	"slices"
	"strconv"
	"strings"

	"github.com/jmmshn/bees/internal/port"
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

// Owns reports whether argv carries --name name, as every launch Prepare builds does.
func (Adapter) Owns(argv []string, name string) bool {
	i := slices.Index(argv, "--name")
	return i >= 0 && i+1 < len(argv) && argv[i+1] == name
}

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

// Observe tells nothing: a resume is --continue, which needs no id from the session.
func (Adapter) Observe([]string) map[string]string { return nil }

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
// Its only env is the shell timeout (BASH_DEFAULT_TIMEOUT_MS and BASH_MAX_TIMEOUT_MS, Claude
// Code's 2-minute default otherwise cuts bs done --wait); the worker's bs finds its
// project from its herdr pane (ADR-0010).
//
// A worker owns its worktree and branch, so the classifier may let it build, test and push
// its own branch (bees-1.51, lost in the rebuild: bees-13.72). Every autoMode list keeps
// "$defaults": a list without it replaces the built-in rules (force push, exfiltration).
// The deny rules run before the classifier; a rule's * matches any text, a space or a
// slash too (probed on Claude Code 2.1.302).
func workerSettings(st port.Start) (string, error) {
	deny := make([]string, len(st.Deny))
	for i, d := range st.Deny {
		deny[i] = "Bash(" + d + ")"
	}
	s := map[string]any{
		"crossSessionInbound": "accept",
		"autoMode": map[string][]string{
			"environment": {"$defaults", environment(st)},
			"allow":       allow(st),
		},
		"permissions": map[string][]string{"deny": deny},
	}
	if st.ShellTimeout > 0 {
		ms := strconv.FormatInt(st.ShellTimeout.Milliseconds(), 10)
		s["env"] = map[string]string{"BASH_DEFAULT_TIMEOUT_MS": ms, "BASH_MAX_TIMEOUT_MS": ms}
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

// environment tells the classifier whose workspace this is, and a push goal's where its
// branch goes; a workspace with no branch claims none.
func environment(st port.Start) string {
	switch {
	case st.Branch != "" && st.PushTo != "":
		return fmt.Sprintf("Host containment: a bees worker, working in its own git worktree %s on its own branch %s of the repository %s; its goal is to push that branch onto branch %s, the head of a pull request it was handed.", st.Dir, st.Branch, st.Repo, st.PushTo)
	case st.Branch == "":
		return fmt.Sprintf("Host containment: a bees worker, working in its own git worktree %s of the repository %s.", st.Dir, st.Repo)
	}
	return fmt.Sprintf("Host containment: a bees worker, working in its own git worktree %s on its own branch %s of the repository %s.", st.Dir, st.Branch, st.Repo)
}

// allow is the routine work of a worker the classifier lets through: its build and
// tests, its own branch's commits, pushes and pull request (a push goal's: pushes onto
// the target's head branch instead), and the bees tools.
func allow(st port.Start) []string {
	a := []string{"$defaults",
		fmt.Sprintf("Running this repository's own build, lint and test commands (such as go vet, go test, go build -o into the worktree, pre-commit) inside the worktree %s.", st.Dir)}
	switch {
	case st.Branch != "" && st.PushTo != "": // a push goal: the target change's head, not a PR of its own (bees-13.78.1)
		a = append(a, fmt.Sprintf("Committing to the worker's own branch %s and pushing it (never force-pushing) onto branch %s, the head of the pull request its goal names, on the remote that holds that head (git push <remote> HEAD:%s), and pushing further commits there.", st.Branch, st.PushTo, st.PushTo))
	case st.Branch != "":
		a = append(a, fmt.Sprintf("Committing to and pushing (never force-pushing) the worker's own branch %s to the repository's origin, opening a pull request from it, and pushing further commits to that open pull request.", st.Branch))
	}
	return append(a, "Running bs and bd, the bees tools, to read and send its project's messages and to read and update its own beads.")
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
