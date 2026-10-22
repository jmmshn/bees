// Package herdr drives the herdr command line for one herdr server (its --session),
// through a port.Runner: the real herdr in bs, the stateful fake in tests, so the argv bs
// builds is checked by the fake the way herdr checks it (motif 13).
package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// Default is the session of the herdr server a pane with no HERDR_SESSION runs on: bs
// passes no --session for it.
const Default = "default"

// Client is one herdr server.
type Client struct {
	Run     port.Runner
	Session string // "" or Default: the default server
}

// Error is herdr's error envelope: a command herdr answered with a refusal.
type Error struct {
	Command string
	Code    string
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("herdr %s: %s (%s)", e.Command, e.Message, e.Code) }

// Codes and errors bs tells apart (probed on herdr 0.9.3–0.9.5).
var (
	ErrDown       = errors.New("herdr didn't answer")                      // no server, or the CLI couldn't run
	ErrPaneGone   = errors.New("pane not found")                           // pane_not_found, agent_pane_not_found
	ErrNameTaken  = errors.New("a live agent already has the name")        // agent_name_taken: herdr started nothing
	ErrPaneBusy   = errors.New("pane is not at an idle shell yet")         // agent_pane_busy: herdr typed nothing (bees-12.33)
	ErrUnready    = errors.New("the agent didn't become ready in time")    // timeout, agent_not_ready: the process may run (bees-12.49)
	ErrDirty      = errors.New("worktree has modified or untracked files") // dirty_worktree_requires_force
	ErrUnsafeArg  = errors.New("herdr can't pass an agent argument containing a control character")
	ErrLongLaunch = errors.New("launch line too long to type into a pane")
)

// codes maps herdr's error codes to the errors above.
var codes = map[string]error{
	"server_not_running":            ErrDown,
	"pane_not_found":                ErrPaneGone,
	"agent_pane_not_found":          ErrPaneGone,
	"agent_name_taken":              ErrNameTaken,
	"agent_pane_busy":               ErrPaneBusy,
	"timeout":                       ErrUnready,
	"agent_not_ready":               ErrUnready,
	"dirty_worktree_requires_force": ErrDirty,
	"invalid_agent_argument":        ErrUnsafeArg,
}

// Unwrap lets errors.Is match a code to its error.
func (e *Error) Unwrap() error { return codes[e.Code] }

// Argv is herdr args on c's server: --session for any but the default one.
func (c Client) Argv(args ...string) []string {
	argv := []string{"herdr"}
	if c.Session != "" && c.Session != Default {
		argv = append(argv, "--session", c.Session)
	}
	return append(argv, args...)
}

// call runs herdr args on c's server and decodes its JSON result into out (if non-nil).
func (c Client) call(ctx context.Context, out any, args ...string) error {
	b, err := c.Run.Run(ctx, "", c.Argv(args...)...)
	if err != nil {
		var ee *port.ExitError
		if !errors.As(err, &ee) {
			return fmt.Errorf("%w: herdr %s: %w", ErrDown, args[0], err)
		}
		var env struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		for _, s := range [][]byte{ee.Stderr, ee.Stdout} {
			if json.Unmarshal(bytes.TrimSpace(s), &env) == nil && env.Error.Code != "" {
				return &Error{Command: strings.Join(args[:min(2, len(args))], " "), Code: env.Error.Code, Message: env.Error.Message}
			}
		}
		return fmt.Errorf("herdr %s: %w", args[0], err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(bytes.TrimSpace(b), out); err != nil {
		return fmt.Errorf("herdr %s: parse output: %w", args[0], err)
	}
	return nil
}

// Workspace is a herdr workspace bs opened, with its root pane.
type Workspace struct {
	ID, Label, Pane string
	Path            string // the worktree's checkout path, or the dir it was opened on
}

// created is herdr's answer to workspace create and worktree create.
type created struct {
	Result struct {
		Workspace struct {
			ID string `json:"workspace_id"`
		} `json:"workspace"`
		RootPane struct {
			ID string `json:"pane_id"`
		} `json:"root_pane"`
		Worktree struct {
			Path string `json:"path"`
		} `json:"worktree"`
	} `json:"result"`
}

func (r created) workspace(label, path string) (Workspace, error) {
	if r.Result.Workspace.ID == "" || r.Result.RootPane.ID == "" {
		return Workspace{}, errors.New("herdr: no workspace or pane id in its output")
	}
	if r.Result.Worktree.Path != "" {
		path = r.Result.Worktree.Path
	}
	return Workspace{ID: r.Result.Workspace.ID, Label: label, Pane: r.Result.RootPane.ID, Path: path}, nil
}

// CreateWorktree makes branch's git worktree at path from repo (base "" is HEAD) and a
// workspace on it. herdr refuses a repo that is itself a linked worktree.
func (c Client) CreateWorktree(ctx context.Context, repo, base, branch, path, label string) (Workspace, error) {
	args := []string{"worktree", "create", "--cwd", repo, "--branch", branch, "--path", path, "--label", label, "--no-focus"}
	if base != "" {
		args = append(args, "--base", base)
	}
	var r created
	if err := c.call(ctx, &r, args...); err != nil {
		return Workspace{}, err
	}
	return r.workspace(label, path)
}

// CreateWorkspace opens a workspace on dir. herdr opens a missing dir as $HOME with exit
// 0 (probed, 0.9.5), so dir must exist: the caller checks.
func (c Client) CreateWorkspace(ctx context.Context, dir, label string) (Workspace, error) {
	var r created
	if err := c.call(ctx, &r, "workspace", "create", "--cwd", dir, "--label", label, "--no-focus"); err != nil {
		return Workspace{}, err
	}
	return r.workspace(label, dir)
}

// MaxToken is how much of a metadata token's value herdr keeps: it cuts the rest without a
// word (bees-12.17).
const MaxToken = 80

// TagWorkspace reports tokens as workspace's metadata, --source last (herdr 0.9.5 takes
// either order, probed in bees-13.2; an older one was seen refusing it first). A value over
// MaxToken is refused here rather than cut silently.
func (c Client) TagWorkspace(ctx context.Context, workspace string, tokens map[string]string) error {
	args := []string{"workspace", "report-metadata", workspace}
	for _, k := range slices.Sorted(maps.Keys(tokens)) {
		if v := tokens[k]; len(v) > MaxToken {
			return fmt.Errorf("herdr token %s is %d bytes; herdr keeps %d", k, len(v), MaxToken)
		}
		args = append(args, "--token", k+"="+tokens[k])
	}
	return c.call(ctx, nil, append(args, "--source", "bs")...)
}

// maxLaunch bounds the agent arguments' bytes. herdr types the agent and its arguments
// into the pane's shell, and keys that land before the shell's line editor is up go
// through the tty's canonical queue, which drops what passes MAX_CANON: 1024 bytes on
// macOS (probed with herdr 0.9.3, bees-8.3). This leaves room for herdr's own command.
const maxLaunch = 768

// CheckAgentArgs applies herdr's rules for agent arguments: herdr agent start refuses any
// argument holding a Unicode control character with invalid_agent_argument (probed on
// 0.9.3; TestCheckAgentArgsMatchesRealHerdr re-checks the installed herdr), and the tty
// cuts a launch past maxLaunch.
func CheckAgentArgs(args []string) error {
	n := 0
	for i, a := range args {
		if Unsafe(a) {
			return fmt.Errorf("agent argument %d (%.40q): %w", i, a, ErrUnsafeArg)
		}
		n += len(a) + 1
	}
	if n > maxLaunch {
		return fmt.Errorf("%d bytes, over %d: %w", n, maxLaunch, ErrLongLaunch)
	}
	return nil
}

// Unsafe reports whether herdr refuses a as an agent argument: it holds a Unicode control
// character (newline, tab, ESC, DEL, C1).
func Unsafe(a string) bool { return strings.IndexFunc(a, unicode.IsControl) >= 0 }

// StartTimeout bounds herdr's wait for the agent to be ready.
const StartTimeout = 10 * time.Second

// StartAgent starts kind with args in pane as agent name. ErrUnready means herdr gave up
// waiting while the process may well run: the caller looks for it (bees-12.49).
func (c Client) StartAgent(ctx context.Context, pane, name, kind string, args ...string) error {
	if err := CheckAgentArgs(args); err != nil {
		return err
	}
	return c.call(ctx, nil, append([]string{"agent", "start", name, "--kind", kind, "--pane", pane,
		"--timeout", strconv.FormatInt(StartTimeout.Milliseconds(), 10), "--"}, args...)...)
}

// PaneProcess is a foreground process herdr found in a pane.
type PaneProcess struct {
	PID  int
	Name string
	Argv []string
}

// PaneProcesses is a pane's shell and its foreground processes.
type PaneProcesses struct {
	Shell      int
	Foreground []PaneProcess
}

// ProcessInfo is pane's processes. A pane herdr doesn't know is ErrPaneGone.
func (c Client) ProcessInfo(ctx context.Context, pane string) (PaneProcesses, error) {
	var r struct {
		Result struct {
			Info struct {
				Shell      int `json:"shell_pid"`
				Foreground []struct {
					PID  int      `json:"pid"`
					Name string   `json:"name"`
					Argv []string `json:"argv"`
				} `json:"foreground_processes"`
			} `json:"process_info"`
		} `json:"result"`
	}
	if err := c.call(ctx, &r, "pane", "process-info", "--pane", pane); err != nil {
		return PaneProcesses{}, err
	}
	out := PaneProcesses{Shell: r.Result.Info.Shell}
	for _, p := range r.Result.Info.Foreground {
		out.Foreground = append(out.Foreground, PaneProcess{PID: p.PID, Name: p.Name, Argv: p.Argv})
	}
	return out, nil
}

// Panes lists workspace's pane ids; "" lists every pane. A workspace that is gone has none.
func (c Client) Panes(ctx context.Context, workspace string) ([]string, error) {
	var r struct {
		Result struct {
			Panes []struct {
				ID string `json:"pane_id"`
			} `json:"panes"`
		} `json:"result"`
	}
	args := []string{"pane", "list"}
	if workspace != "" {
		args = append(args, "--workspace", workspace)
	}
	err := c.call(ctx, &r, args...)
	if he := (*Error)(nil); errors.As(err, &he) && he.Code == "workspace_not_found" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range r.Result.Panes {
		out = append(out, p.ID)
	}
	return out, nil
}

// ClosePane closes pane; one already gone is closed.
func (c Client) ClosePane(ctx context.Context, pane string) error {
	return gone(c.call(ctx, nil, "pane", "close", pane), "pane_not_found")
}

// CloseWorkspace closes workspace and its panes, keeping its directory; one already gone
// is closed.
func (c Client) CloseWorkspace(ctx context.Context, workspace string) error {
	return gone(c.call(ctx, nil, "workspace", "close", workspace), "workspace_not_found")
}

// RemoveWorktree removes a worktree workspace and its checkout. Without force herdr
// refuses one with modified or untracked files (ErrDirty); one already gone is removed.
func (c Client) RemoveWorktree(ctx context.Context, workspace string, force bool) error {
	args := []string{"worktree", "remove", "--workspace", workspace}
	if force {
		args = append(args, "--force")
	}
	return gone(c.call(ctx, nil, args...), "workspace_not_found")
}

// gone is err, or nil when herdr refused with code (the thing is already gone).
func gone(err error, code string) error {
	if he := (*Error)(nil); errors.As(err, &he) && he.Code == code {
		return nil
	}
	return err
}
