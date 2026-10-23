// Package config is bs's config (ADR-0010 Config, on ADR-0008's loader): a global
// config.toml and projects/<project>.toml under the config dir, declared once as a tagged
// struct. Unlike main's internal/config it reads no process state: the caller's env comes
// in, so a toy's bs reads its own session's dir and nothing else (bees-12.26).
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/goal"
)

// Config is every setting bs reads; its tables mirror the TOML tables. Each string,
// bool or list leaf is tagged with:
//
//   - toml: its key within its table;
//   - scope: which file may set it: global, project or both;
//   - default: its value when nothing sets it (absent means ""); a list's is one item;
//   - env: the variable that overrides both files outside a recorded herdr pane (optional);
//   - enum: its allowed values, separated by | ("" always passes where it is the default);
//   - path: a path: "dir", or "{repo}" when it may begin with the {repo} placeholder;
//   - glob: present on a pattern over paths relative to a worktree (path.Match syntax);
//   - unread: present when bs doesn't act on the key yet; its value names the bead that
//     wires it. bs config marks it and bs doctor warns when a file sets it;
//   - doc: what it means, for bs config init (bees-13.8) to write as a comment.
//
// A table (a pointer to a struct, or a map of them) is a unit: the nearest file that
// sets it sets all of it, and its own check method validates it.
type Config struct {
	Harness   string              `toml:"harness" scope:"global" default:"claude_code" enum:"claude_code" doc:"the harness every queen and worker on this machine starts on (omp and agy arrive with bees-12.8 and bees-12.12)"`
	Paths     Paths               `toml:"paths"`
	Workspace Workspace           `toml:"workspace"`
	Goal      map[string]GoalType `toml:"goal" scope:"both" unread:"bees-13.11" doc:"custom goal types, by name"`
	Checks    map[string]Check    `toml:"checks" scope:"both" unread:"bees-13.11" doc:"argv a custom goal's cmd:<name> check runs"`
	Guards    Guards              `toml:"guards"`
	Review    Review              `toml:"review"`
	Queen     Role                `toml:"queen"`
	Worker    Worker              `toml:"worker"`
	Prune     Prune               `toml:"prune"`
}

// Paths says where bs keeps its files and finds repos.
type Paths struct {
	LogbookDir string `toml:"logbook_dir" scope:"global" env:"BS_LOGBOOK_DIR" default:"~/.local/share/bees" path:"dir" doc:"where each project's logbook, <project>.db, lives"`
	Repos      string `toml:"repos" scope:"global" env:"BS_REPOS" path:"dir" doc:"a directory of repos, where a project's repo is found by its normalised name"`
	Repo       string `toml:"repo" scope:"project" path:"dir" doc:"the project's directory, for commands run outside it"`
	Worktrees  string `toml:"worktrees" scope:"both" default:"{repo}-WT" path:"{repo}" doc:"where git_worktree workspaces go, as <worktrees>/<worker>; {repo} is the main checkout"`
}

// Workspace says what a worker's workspace is. The kind is worked out (Kind); the key
// only overrides it.
type Workspace struct {
	Kind    string   `toml:"kind" scope:"both" enum:"git_worktree|none|command" doc:"overrides the worked-out kind: command when [workspace.command] is set, else git_worktree in a git checkout's top level, else none; spawn makes only git_worktree until bees-13.10 (none) and bees-13.11 (command)"`
	Command *Command `toml:"command" scope:"both" unread:"bees-13.11" doc:"argv that make and remove a command workspace (cwd: paths.repo)"`
}

// Command is [workspace.command]: its presence makes the kind command.
type Command struct {
	Create  []string `toml:"create"`
	Remove  []string `toml:"remove"`
	Clean   []string `toml:"clean"` // exits 0 when nothing would be lost: required
	Dir     string   `toml:"dir"`
	Timeout string   `toml:"timeout"`
}

// GoalType is a custom goal type, [goal.<name>]: the checks each stage runs, by name (a
// built-in check, or cmd:<name> for [checks.<name>]), and its prompt fragment.
type GoalType struct {
	Handoff  []string `toml:"handoff"`
	Complete []string `toml:"complete"`
	Prune    []string `toml:"prune"`
	Fragment string   `toml:"fragment"`
}

// Check is [checks.<name>]: an argv, never a shell, and its bound.
type Check struct {
	Run     []string `toml:"run"`
	Timeout string   `toml:"timeout"`
}

// Guards add to (or with Replace, replace) the workspace's default deny list, and grant
// merges into the bases AllowMergeInto names.
type Guards struct {
	Deny           []string `toml:"deny" scope:"both" doc:"shell globs over a command line every worker is denied, added to the workspace's default"`
	Replace        bool     `toml:"replace" scope:"both" doc:"deny replaces the default instead of adding to it"`
	AllowMergeInto []string `toml:"allow_merge_into" scope:"both" doc:"base-branch globs a worker may merge its own PR into: the worker is told them, and the merge deny is lifted for every base (a deny glob can't see a PR's base), so it is refused unless the review host protects the default branch, and a glob matching a default branch is refused"`
}

// Review says where a project's changes (pr, push and review goals) are reviewed and land.
type Review struct {
	Host     string `toml:"host" scope:"both" default:"github" enum:"github" doc:"the review host pr, push and review goals check against: github (pull requests through gh, the repo's git remotes)"`
	Fragment string `toml:"fragment" scope:"both" path:"dir" doc:"a markdown file that is the review host's section of the prompt, in place of the built-in one: how a worker uploads, revises and hands off a change there, and how the queen lands it (lines <!-- worker --> and <!-- queen --> start each role's part). A host with no built-in section (a command host) gets its instructions only from here, so no host's workflow ships in bs"`
}

// Role holds the queen's harness tables.
type Role struct {
	ClaudeCode ClaudeCode `toml:"claude_code"`
}

// Prune is what bs prune lets go.
type Prune struct {
	Ignore []string `toml:"ignore" scope:"both" default:"bin/" glob:"" doc:"ignored files in a worktree that prune removes with it (build output), as .gitignore spells them: a pattern ending in / is a directory and all under it, one with no other / matches at any depth, one with an inner / from the worktree's top; frozen at spawn. Every other ignored file keeps the worktree, named (a report in an ignored scratch/)"`
}

// Worker holds every worker's harness tables and bs spawn's own settings.
type Worker struct {
	Path       []string   `toml:"path" scope:"project" path:"dir" doc:"dirs put first on every worker's PATH (a live test's build under test)"`
	ClaudeCode ClaudeCode `toml:"claude_code"`
}

// ClaudeCode holds the settings bs passes to Claude Code.
type ClaudeCode struct {
	Model string `toml:"model" scope:"both" doc:"the model Claude Code starts on (claude --model); empty: Claude Code's default"`
}

// The workspace kinds.
const (
	GitWorktree = "git_worktree"
	NoWorkspace = "none"
	CommandKind = "command"
)

// Kind is the workspace kind and why: the override, else command when
// [workspace.command] is set (both chosen by the config), else worked out (Rule D):
// git_worktree when the project dir is the top level of a git checkout, else none.
func (l *Loaded) Kind(gitTop bool) (kind, why string, chosen bool) {
	switch {
	case l.Workspace.Kind != "":
		return l.Workspace.Kind, "workspace.kind, " + l.Sources["workspace.kind"].String(), true
	case l.Workspace.Command != nil:
		return CommandKind, "[workspace.command] is set, " + l.Sources["workspace.command"].String(), true
	case gitTop:
		return GitWorktree, "worked out: the repo is a git checkout's top level", false
	}
	return NoWorkspace, "worked out: no repo that is a git checkout's top level", false
}

// check checks only what config can know. A missing argv or dir is refused where it
// would run, at spawn (ADR-0010), so bs config and doctor still show the table.
func (c *Command) check() error { return checkTimeout(c.Timeout) }

// Missing names each of c's required keys left unset: create, remove, clean (exits 0
// when nothing would be lost) and dir.
func (c *Command) Missing() []string {
	var out []string
	for _, a := range []struct {
		key  string
		argv []string
	}{{"create", c.Create}, {"remove", c.Remove}, {"clean", c.Clean}} {
		if len(a.argv) == 0 || a.argv[0] == "" {
			out = append(out, a.key)
		}
	}
	if c.Dir == "" {
		out = append(out, "dir")
	}
	return out
}

func (g GoalType) check(name string, checks map[string]Check) error {
	if goal.IsType(name) {
		return fmt.Errorf("%s is a built-in goal type", name)
	}
	var errs []error
	for _, s := range []struct {
		key   string
		names []string
	}{{"handoff", g.Handoff}, {"complete", g.Complete}, {"prune", g.Prune}} {
		for _, n := range s.names {
			cmd, ok := strings.CutPrefix(n, "cmd:")
			switch _, defined := checks[cmd]; {
			case ok && !defined:
				errs = append(errs, fmt.Errorf("%s: %q names no [checks.%s]", s.key, n, cmd))
			case !ok && !goal.IsCheck(n):
				errs = append(errs, fmt.Errorf("%s: %q is no built-in check; a command check is cmd:<name>", s.key, n))
			}
		}
	}
	return errors.Join(errs...) // fragment is a path: Load checks it as every path is
}

func (c Check) check() error {
	if len(c.Run) == 0 || c.Run[0] == "" {
		return errors.New("run: an argv is required")
	}
	return checkTimeout(c.Timeout)
}

// checkTimeout refuses a timeout time.ParseDuration can't read, or one not above zero;
// "" is the default bound.
func checkTimeout(s string) error {
	if s == "" {
		return nil
	}
	if d, err := time.ParseDuration(s); err != nil || d <= 0 {
		return fmt.Errorf("timeout = %q: want a duration above zero, e.g. \"60s\"", s)
	}
	return nil
}
