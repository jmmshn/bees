// Package config is bs's config (ADR-0010 Config, on ADR-0008's loader): a global
// config.toml and projects/<project>.toml under the config dir, declared once as a tagged
// struct. Unlike main's internal/config it reads no process state: the caller's env comes
// in, so a toy's bs reads its own session's dir and nothing else (bees-12.26).
package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/checks"
	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/vcs/command"
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
//   - program: a program, its base name the value: that name on PATH, or an absolute path;
//   - glob: present on a pattern over paths relative to a worktree (path.Match syntax);
//   - unread: present when bs doesn't act on the key yet; its value names the bead that
//     wires it. bs config marks it and bs doctor warns when a file sets it;
//   - doc: what it means, for bs config init (bees-13.8) to write as a comment.
//
// A table (a pointer to a struct, or a map of them) is a unit: the nearest file that
// sets it sets all of it, and its own check method validates it.
type Config struct {
	Harness   string              `toml:"harness" scope:"global" default:"claude_code" enum:"claude_code|agy" doc:"the harness every queen and worker on this machine starts on: claude_code (Claude Code) or agy (Antigravity's CLI; omp arrives with bees-12.8)"`
	Agy       AgyInstall          `toml:"agy"`
	Paths     Paths               `toml:"paths"`
	Workspace Workspace           `toml:"workspace"`
	Goal      map[string]GoalType `toml:"goal" scope:"both" doc:"custom goal types, by name: --goal <name> runs their checks"`
	Checks    map[string]Check    `toml:"checks" scope:"both" doc:"argv a custom goal's cmd:<name> check runs in the workspace: exit 0 is yes, any other exit no"`
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
	Kind    string   `toml:"kind" scope:"both" enum:"git_worktree|none|command" doc:"overrides the worked-out kind: command when [workspace.command] is set, else git_worktree in a git checkout's top level, else none; spawn makes none only once bees-13.10 lands"`
	Command *Command `toml:"command" scope:"both" doc:"argv that make (create), remove (remove) and check (clean: exit 0 when nothing would be lost) a command workspace at dir; create and remove run in paths.repo, clean in the workspace. Optional, all four or none: branch, pushed, has and descends answer the VCS facts pr goals read, each one JSON object on stdout (docs/SETUP.md)"`
}

// Command is [workspace.command]: its presence makes the kind command.
type Command struct {
	Create  []string `toml:"create"`
	Remove  []string `toml:"remove"`
	Clean   []string `toml:"clean"` // exits 0 when nothing would be lost: required
	Dir     string   `toml:"dir"`
	Timeout string   `toml:"timeout"`
	// The workspace's VCS (bees-14.4), all four or none: each prints one JSON object
	// (internal/vcs/command). With none, a goal that reads a branch is refused.
	Branch   []string `toml:"branch"`
	Pushed   []string `toml:"pushed"`
	Has      []string `toml:"has"`
	Descends []string `toml:"descends"`
}

// vcs is c's VCS argv, by key.
func (c *Command) vcs() []struct {
	key  string
	argv []string
} {
	return []struct {
		key  string
		argv []string
	}{{"branch", c.Branch}, {"pushed", c.Pushed}, {"has", c.Has}, {"descends", c.Descends}}
}

// VCS reports whether c sets its VCS argv (load refused a partial set).
func (c *Command) VCS() bool { return c != nil && len(c.Branch) > 0 }

// VCSCmds is c's VCS argv as a spawn freezes them, each with c's timeout; nil when unset.
func (c *Command) VCSCmds(timeout time.Duration) *command.Cmds {
	if !c.VCS() {
		return nil
	}
	cmd := func(argv []string) checks.Cmd { return checks.Cmd{Argv: slices.Clone(argv), Timeout: timeout} }
	return &command.Cmds{Branch: cmd(c.Branch), Pushed: cmd(c.Pushed), Has: cmd(c.Has), Descends: cmd(c.Descends)}
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
	Host     string         `toml:"host" scope:"both" default:"github" enum:"github|command" doc:"the review host pr, push and review goals check against: github (pull requests through gh, the repo's git remotes) or command (your own argv in [review.command], answering in JSON)"`
	Fragment string         `toml:"fragment" scope:"both" path:"dir" doc:"a markdown file that is the review host's section of the prompt, in place of the built-in one: how a worker uploads, revises and hands off a change there, and how the queen lands it (lines <!-- worker --> and <!-- queen --> start each role's part). A host with no built-in section (a command host) gets its instructions only from here, so no host's workflow ships in bs"`
	Command  *ReviewCommand `toml:"command" scope:"both" doc:"review.host = \"command\": the argv that answer for the review host, each printing one JSON value (docs/SETUP.md), the URL regexp, the hints shown to a worker and the host's own deny globs"`
}

// CommandHost is review.host's value for [review.command].
const CommandHost = "command"

// ReviewCommand is [review.command] (ADR-0010 Amendment 6): argv that answer for a
// review host in JSON, run in the project's checkout.
type ReviewCommand struct {
	URL         string   `toml:"url"` // a regexp with named groups repo and number
	Repo        []string `toml:"repo"`
	Changes     []string `toml:"changes"`
	View        []string `toml:"view"`
	CI          []string `toml:"ci"`
	PostReview  []string `toml:"post_review"`
	Fetch       []string `toml:"fetch"`
	Create      string   `toml:"create"`
	Retarget    string   `toml:"retarget"`
	PushHint    string   `toml:"push_hint"`
	MergeDeny   []string `toml:"merge_deny"`
	ApproveDeny []string `toml:"approve_deny"`
	Timeout     string   `toml:"timeout"`
}

// The placeholders each [review.command] key may name: every argv runs in the project's
// checkout ({repo}).
var (
	ReviewChangesKeys = []string{"repo", "dir", "branch", "base"}
	ReviewViewKeys    = []string{"repo", "url"}
	ReviewPostKeys    = []string{"repo", "url", "body_file", "marker"}
	ReviewFetchKeys   = []string{"repo", "url", "commit"}
	ReviewHintKeys    = []string{"url", "number", "base", "dir", "branch", "head"}
)

// argvs is each of r's argv keys with the placeholders it may name.
func (r *ReviewCommand) argvs() []struct {
	key  string
	argv []string
	keys []string
} {
	return []struct {
		key  string
		argv []string
		keys []string
	}{{"repo", r.Repo, []string{"repo"}}, {"changes", r.Changes, ReviewChangesKeys}, {"view", r.View, ReviewViewKeys},
		{"ci", r.CI, ReviewViewKeys}, {"post_review", r.PostReview, ReviewPostKeys}, {"fetch", r.Fetch, ReviewFetchKeys}}
}

// check checks what config can know of a [review.command]: each argv's placeholders, the
// URL regexp and its groups, the hints' placeholders, the timeout. A missing key is
// Incomplete's, refused only when review.host is command.
func (r *ReviewCommand) check() error {
	errs := []error{checkTimeout(r.Timeout)}
	for _, a := range r.argvs() {
		if len(a.argv) > 0 {
			if err := checks.Check(a.argv, a.keys); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", a.key, err))
			}
		}
	}
	if r.URL != "" {
		if _, err := URLPattern(r.URL); err != nil {
			errs = append(errs, err)
		}
	}
	for _, h := range []struct{ key, text string }{{"create", r.Create}, {"retarget", r.Retarget}, {"push_hint", r.PushHint}} {
		rest := h.text
		for _, k := range ReviewHintKeys {
			rest = strings.ReplaceAll(rest, "{"+k+"}", "")
		}
		if strings.ContainsAny(rest, "{}") {
			errs = append(errs, fmt.Errorf("%s = %q: only {%s} are filled in", h.key, h.text, strings.Join(ReviewHintKeys, "}, {")))
		}
	}
	return errors.Join(errs...)
}

// URLPattern compiles [review.command] url, matched against a whole URL: it must name
// the groups repo and number.
func URLPattern(s string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(`^(?:` + s + `)$`)
	if err != nil {
		return nil, fmt.Errorf("url = %q: %w", s, err)
	}
	for _, g := range []string{"repo", "number"} {
		if re.SubexpIndex(g) < 0 {
			return nil, fmt.Errorf("url = %q: it must name a group (?P<%s>…)", s, g)
		}
	}
	return re, nil
}

// Incomplete is the error of a [review.command] missing a required key (url, repo,
// changes, view, ci, post_review), nil when none is.
func (r *ReviewCommand) Incomplete() error {
	if r == nil {
		return errors.New(`review.host = "command" needs a [review.command] table`)
	}
	var miss []string
	if r.URL == "" {
		miss = append(miss, "url")
	}
	for _, a := range r.argvs() {
		if a.key != "fetch" && (len(a.argv) == 0 || a.argv[0] == "") {
			miss = append(miss, a.key)
		}
	}
	if len(miss) > 0 {
		return fmt.Errorf("[review.command] has no %s", strings.Join(miss, ", "))
	}
	return nil
}

// Role holds the queen's harness tables.
type Role struct {
	ClaudeCode ClaudeCode `toml:"claude_code"`
	Agy        Agy        `toml:"agy"`
}

// Prune is what bs prune lets go.
type Prune struct {
	Ignore []string `toml:"ignore" scope:"both" default:"bin/" glob:"" doc:"ignored files in a worktree that prune removes with it (build output), as .gitignore spells them: a pattern ending in / is a directory and all under it, one with no other / matches at any depth, one with an inner / from the worktree's top; frozen at spawn. Every other ignored file keeps the worktree, named (a report in an ignored scratch/)"`
}

// Worker holds every worker's harness tables and bs spawn's own settings.
type Worker struct {
	Path       []string   `toml:"path" scope:"project" path:"dir" doc:"dirs put first on every worker's PATH (a live test's build under test)"`
	ClaudeCode ClaudeCode `toml:"claude_code"`
	Agy        Agy        `toml:"agy"`
}

// ClaudeCode holds the settings bs passes to Claude Code.
type ClaudeCode struct {
	Model string `toml:"model" scope:"both" doc:"the model Claude Code starts on (claude --model); empty: Claude Code's default"`
}

// Agy holds the settings bs passes to agy.
type Agy struct {
	Model string `toml:"model" scope:"both" doc:"the model agy starts on (agy --model); empty: agy's default"`
}

// AgyInstall is the machine's agy: an agy-compatible build lives at its own path and
// keeps its own settings, so neither is hard-coded (bees-14.1).
type AgyInstall struct {
	Bin      string `toml:"bin" scope:"global" default:"agy" program:"agy" doc:"the agy program: a name on PATH or an absolute path (~ expanded); its base name must be agy, the program herdr's --kind agy starts"`
	Settings string `toml:"settings" scope:"global" default:"~/.gemini/antigravity-cli/settings.json" path:"dir" doc:"agy's settings.json, whose trustedWorkspaces lists the folders agy trusts"`
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

// check checks only what config can know: each argv's placeholders, the dir's, the
// timeout. A missing argv or dir is refused where it would run, at spawn (ADR-0010), so
// bs config and doctor still show the table.
func (c *Command) check() error {
	errs := []error{checkTimeout(c.Timeout)}
	for _, a := range []struct {
		key  string
		argv []string
	}{{"create", c.Create}, {"remove", c.Remove}, {"clean", c.Clean}} {
		if len(a.argv) == 0 {
			continue
		}
		if err := checks.Check(a.argv, checks.WorkspaceKeys); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.key, err))
		}
	}
	var set, unset []string
	for _, a := range c.vcs() {
		if len(a.argv) == 0 {
			unset = append(unset, a.key)
			continue
		}
		set = append(set, a.key)
		if err := checks.Check(a.argv, command.Keys); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.key, err))
		}
	}
	if len(set) > 0 && len(unset) > 0 {
		errs = append(errs, fmt.Errorf("sets %s but not %s: the workspace's VCS is all four of branch, pushed, has and descends, or none", strings.Join(set, ", "), strings.Join(unset, ", ")))
	}
	if c.Dir != "" {
		errs = append(errs, checkDir(c.Dir))
	}
	return errors.Join(errs...)
}

// checkDir refuses a [workspace.command] dir that names a placeholder other than {repo}
// and {name}, or that doesn't name {name}: every worker gets a directory of its own.
func checkDir(dir string) error {
	switch rest := FillDir(dir, "", ""); {
	case strings.ContainsAny(rest, "{}"):
		return fmt.Errorf("dir = %q: only {repo} and {name} are filled in", dir)
	case !strings.Contains(dir, "{name}"):
		return fmt.Errorf("dir = %q: it must name {name}, so each worker gets a directory of its own", dir)
	}
	return nil
}

// Incomplete is the error of a [workspace.command] missing a required key, nil when none
// is: spawn refuses it, doctor fails it.
func (c *Command) Incomplete() error {
	if c == nil {
		return errors.New("no [workspace.command] table")
	}
	if miss := c.Missing(); len(miss) > 0 {
		return fmt.Errorf("[workspace.command] has no %s (clean exits 0 when nothing in the workspace would be lost: it is what lets prune remove one)", strings.Join(miss, ", "))
	}
	return nil
}

// FillDir is [workspace.command] dir for worker name of the project at repo: each {repo}
// and {name} filled, the only placeholders it may name, anywhere in it.
func FillDir(dir, repo, name string) string {
	return strings.NewReplacer("{repo}", repo, "{name}", name).Replace(dir)
}

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
			cmd := goal.Cmd(n)
			switch _, defined := checks[cmd]; {
			case cmd != "" && !defined:
				errs = append(errs, fmt.Errorf("%s: %q names no [checks.%s]", s.key, n, cmd))
			case cmd == "" && !goal.IsCheck(n):
				errs = append(errs, fmt.Errorf("%s: %q is no built-in check; a command check is cmd:<name>", s.key, n))
			}
		}
	}
	return errors.Join(errs...) // fragment is a path: Load checks it as every path is
}

func (c Check) check() error {
	if err := checks.Check(c.Run, checks.Keys); err != nil {
		return fmt.Errorf("run: %w", err)
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
