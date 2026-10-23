package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/harness/agy"
	"github.com/jmmshn/bees/internal/port"
)

// errNoConfigDir is a session whose config dir is missing (Rule G): every command that
// opens a logbook refuses, since the defaults would name the real one; config and doctor
// run on the defaults, warning.
var errNoConfigDir = errors.New("no config dir for this herdr session")

// settings is what a command runs on: the config and everything bs worked out around it.
type settings struct {
	*config.Loaded
	Dir, Session string
	Missing      bool   // the session's config dir doesn't exist
	Project      string // "" when bs can't tell: config and doctor still run
	ProjectFrom  string // how bs found it
	Repo         string // the project's main checkout, "" if unknown
	RepoFrom     string
	RepoMissing  bool        // paths.repo or paths.repos named a dir that doesn't exist
	GitTop       bool        // Repo is a git checkout's top level
	Logbook      string      // the logbook file
	Worktrees    string      // paths.worktrees with {repo} and ~ resolved
	Caller       core.Caller // who runs bs, as Resolve found it (before --as)
	PinnedDir    string      // BS_LOGBOOK_DIR outside a recorded pane, when it isn't paths.logbook_dir
	ConfigDir    string      // paths.logbook_dir as the config files set it, without the pin
	Warnings     []string
	Broken       error // why bs couldn't work out its settings: bs doctor still runs
	GlobalBroken bool  // with Broken: the global file didn't read either, so doctor runs on the defaults
}

// broken is the settings bs doctor runs on when load failed with err: the session's dir
// and the default config, so the checks that need no config still run.
func broken(env Env, err error) (*settings, error) {
	getenv := func(k string) string { return core.Getenv(env.Vars, k) }
	dir, session, derr := config.Dir(getenv)
	if derr != nil {
		return nil, err
	}
	// The global file alone, when it reads: a refused project (bees-12.45) leaves the
	// machine checks on the keeper's own settings, not the defaults.
	l, gerr := config.Load(config.Input{Dir: dir, Home: getenv("HOME")})
	if gerr != nil {
		l = config.Defaults(getenv("HOME"))
	}
	s := &settings{Loaded: l, Dir: dir, Session: session, Broken: err, GlobalBroken: gerr != nil}
	if _, serr := os.Stat(dir); errors.Is(serr, os.ErrNotExist) && session != config.DefaultSession {
		s.Missing = true
	}
	return s, nil
}

// envKeys are the BS_* variables the keeper's bs reads. Under a recorded session it reads
// none, and warns of every BS_* set, the old bs's pins included: a session acts in the repo
// it runs in (ADR-0010 Amendment 4), and an inherited pin from some other session's env is
// exactly what bees-12.47 and bees-13.1's probe saw leak.
var envKeys = []string{"BS_PROJECT", "BS_LOGBOOK_DIR", "BS_REPOS"}

// load works out env's settings: its config dir (per herdr session), its project (the git
// repo bs runs in; else, for a recorded session, its own project, and for the keeper
// BS_PROJECT), who runs it (core.Resolve), the files, the repo and the logbook. It fails
// only on what no command can run past: a broken file, a refused dir, a file or dir it
// couldn't read (exit 6), a pane bs can't tell the session of. The settings come back
// with the error, for the warnings found before it.
func load(ctx context.Context, env Env, ports Ports) (*settings, error) {
	run := ports.Run
	dir, session, err := config.Dir(func(k string) string { return core.Getenv(env.Vars, k) })
	if err != nil {
		return nil, err
	}
	s := &settings{Dir: dir, Session: session}
	if fi, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) && session == config.DefaultSession {
		// The keeper's own config: no files yet means the defaults, the real logbooks.
	} else if errors.Is(err, os.ErrNotExist) {
		s.Missing = true
		s.Warnings = append(s.Warnings, fmt.Sprintf("%v %s: %s is missing, so only config and doctor run, on the defaults\n  next: %s config init --logbook-dir <dir>",
			errNoConfigDir, session, dir, bsname.Cmd))
	} else if err != nil {
		return s, cantRead(err)
	} else if !fi.IsDir() {
		return s, fmt.Errorf("config dir %s is not a directory", dir)
	}
	home := core.Getenv(env.Vars, "HOME")
	overrides := map[string]string{}
	for _, kv := range env.Vars {
		if k, v, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "BS_") && v != "" {
			overrides[k] = v
		}
	}
	// The project comes first, from the repo bs runs in (ADR-0010 Amendment 4): every call
	// acts in it, whoever calls; then who calls, in it.
	if err := s.findProject(ctx, env.Dir, run, ""); err != nil {
		return s, err
	}
	if ports.Proc != nil {
		if err := s.resolve(ctx, env, ports, dir, home); err != nil {
			return s, err
		}
	}
	if s.Caller.Name == "" { // no process table to walk: config and doctor in tests
		s.Caller = core.Caller{Name: core.Keeper, Role: core.KeeperR, How: core.ByNobody}
	}
	if s.Caller.Recorded() {
		for k := range overrides {
			s.Warnings = append(s.Warnings, fmt.Sprintf("ignoring %s: %s, a recorded session, runs this bs, so the repo it runs in decides", k, s.Caller.Name))
		}
		clear(overrides)
		if s.Project == "" { // outside any repo, a session acts in its own project (OQ-K6)
			s.Project, s.ProjectFrom = s.Caller.Project, "the project whose logbook ("+s.Caller.Logbook+") records "+s.Caller.Name
			s.Repo, s.RepoFrom = s.Caller.Repo, "the repo "+s.Caller.Name+"'s entry recorded"
		}
	} else if err := s.findProject(ctx, "", run, overrides["BS_PROJECT"]); err != nil {
		return s, err
	}
	for k := range overrides {
		if !slices.Contains(envKeys, k) {
			delete(overrides, k)
		}
	}
	if pin := overrides["BS_LOGBOOK_DIR"]; pin != "" {
		unpinned, err := config.Load(config.Input{Dir: dir, Project: s.Project, Home: home})
		if err != nil {
			return nil, err
		}
		if want := unpinned.Expand(unpinned.Paths.LogbookDir); !samePath(unpinned.Expand(pin), want) {
			s.PinnedDir, s.ConfigDir = pin, want
		}
	}
	l, err := config.Load(config.Input{Dir: dir, Project: s.Project, Home: home, Env: overrides})
	if err != nil {
		return s, cantRead(err)
	}
	s.Loaded = l
	if err := s.findRepo(ctx, run); err != nil {
		return s, err
	}
	if s.Project != "" {
		s.Logbook = filepath.Join(l.Expand(l.Paths.LogbookDir), s.Project+".db")
	}
	if wt, ok := strings.CutPrefix(l.Paths.Worktrees, config.RepoPlaceholder); !ok {
		s.Worktrees = l.Expand(wt)
	} else if s.Repo != "" {
		s.Worktrees = s.Repo + wt
	}
	return s, nil
}

// findProject names the project from the git repo bs runs in, else BS_PROJECT. Both set
// and different is refused (bees-12.47): a pin from another session must never silently
// beat the repo you are in.
func (s *settings) findProject(ctx context.Context, cwd string, run port.Runner, pinned string) error {
	if cwd != "" && s.Project == "" {
		main, err := mainCheckout(ctx, run, cwd)
		switch {
		case errors.Is(err, errNotGit):
		case errors.As(err, new(*core.Exit)):
			return err
		case err != nil: // "couldn't look" is never "no project" (Rule G)
			return &core.Exit{Code: core.ExitCantCheck, Msg: bsname.Cmd + " can't tell which project " + cwd + " is in", Err: err}
		default:
			s.Project, s.ProjectFrom, s.Repo, s.RepoFrom = config.Normalise(filepath.Base(main)), "the git repo at "+main, main, "the git repo "+bsname.Cmd+" runs in"
		}
	}
	switch p := config.Normalise(pinned); {
	case p == "":
	case s.Project == "":
		s.Project, s.ProjectFrom = p, "env BS_PROJECT"
	case p != s.Project:
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("BS_PROJECT=%s names another project than the repo you are in (%s)", pinned, s.Project),
			Next: "env -u BS_PROJECT " + bsname.Cmd + " ..., or run it from " + p + "'s repo"}
	}
	return nil
}

// findRepo is ADR-0008's repo chain: the git repo bs runs in when it is the project's,
// else paths.repo, else the project's dir under paths.repos.
func (s *settings) findRepo(ctx context.Context, run port.Runner) error {
	switch {
	case s.Repo != "":
	case s.Paths.Repo != "":
		s.Repo, s.RepoFrom = s.Expand(s.Paths.Repo), "paths.repo, "+s.Sources["paths.repo"].String()
	case s.Paths.Repos != "" && s.Project != "":
		dir := s.Expand(s.Paths.Repos)
		es, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return cantRead(fmt.Errorf("paths.repos %s: %w", dir, err))
		}
		for _, e := range es {
			if e.IsDir() && config.Normalise(e.Name()) == s.Project {
				s.Repo, s.RepoFrom = filepath.Join(dir, e.Name()), "paths.repos, "+s.Sources["paths.repos"].String()
				break
			}
		}
	}
	if s.Repo == "" {
		return nil
	}
	if _, err := os.Stat(s.Repo); errors.Is(err, os.ErrNotExist) { // git can't even start in it
		s.RepoMissing = true
		s.Warnings = append(s.Warnings, fmt.Sprintf("the repo %s (%s) doesn't exist, so bs spawn refuses", s.Repo, s.RepoFrom))
		return nil
	} else if err != nil {
		return cantRead(err)
	}
	top, err := run.Run(ctx, s.Repo, "git", "-C", s.Repo, "rev-parse", "--show-toplevel")
	switch err := notGit(err); {
	case errors.Is(err, errNotGit):
	case err != nil: // a failing git must not quietly make the kind none
		return &core.Exit{Code: core.ExitCantCheck, Msg: bsname.Cmd + " can't tell whether " + s.Repo + " is a git checkout", Err: err}
	default:
		s.GitTop = samePath(strings.TrimSpace(string(top)), s.Repo)
	}
	return nil
}

// cantRead is err as exit 6 when bs couldn't read a file or dir in it (Rule G: "couldn't
// look" is not "refused"); a broken file stays a refusal.
func cantRead(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return &core.Exit{Code: core.ExitCantCheck, Msg: bsname.Cmd + " couldn't read its config", Err: err}
	}
	return err
}

// errNotGit is a dir git says is in no repo, or a dir that doesn't exist: an answer, not
// a failure to look.
var errNotGit = errors.New("not in a git repo")

// notGit is err as errNotGit when git's answer was "not a git repository" (or no such
// dir); any other failure, git missing included, stays as it is.
func notGit(err error) error {
	var ee *port.ExitError
	if errors.As(err, &ee) && (strings.Contains(string(ee.Stderr), "not a git repository") || strings.Contains(string(ee.Stderr), "No such file or directory")) {
		return errNotGit
	}
	return err
}

// mainCheckout is the main checkout of the git repo holding dir: its own top level in
// the main checkout (whatever the git dir is called), the parent of a .git common dir in a
// linked worktree. A linked worktree of a bare repo or a separate git dir has a main
// checkout git doesn't record: refused, as is a bare repo.
func mainCheckout(ctx context.Context, run port.Runner, dir string) (string, error) {
	out, err := run.Run(ctx, dir, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
	var ee *port.ExitError
	if errors.As(err, &ee) && strings.Contains(string(ee.Stderr), "must be run in a work tree") {
		return "", &core.Exit{Code: core.ExitRefused, Msg: dir + " is in a bare repo, which has no checkout to name a project by", Next: "cd into a checkout of it"}
	}
	if err != nil {
		return "", notGit(err)
	}
	f := strings.Fields(string(out))
	if len(f) != 3 {
		return "", fmt.Errorf("git rev-parse in %s: unexpected output %q", dir, out)
	}
	top, gitDir, common := f[0], f[1], f[2]
	switch {
	case gitDir == common:
		return top, nil
	case filepath.Base(common) == ".git":
		return filepath.Dir(common), nil
	}
	return "", &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("%s is a linked worktree of %s, whose main checkout git doesn't record", top, common),
		Next: "run " + bsname.Cmd + " in the main checkout, or from the keeper's shell with BS_PROJECT set"}
}

// checkLogbook checks s names a project and, on a herdr --session toy, that its logbook
// isn't a real one, which a toy must never migrate or write.
func (s *settings) checkLogbook(env Env) error {
	if err := s.needProject(); err != nil {
		return err
	}
	if s.Session == config.DefaultSession {
		return nil
	}
	for _, real := range realLogbookDirs(env.Vars) {
		if samePath(filepath.Dir(s.Logbook), real) {
			return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("a herdr --session toy's paths.logbook_dir is the real logbooks' dir, %s (%s); nothing was read or written", s.Paths.LogbookDir, s.Sources["paths.logbook_dir"]),
				Next: "set paths.logbook_dir in " + filepath.Join(s.Dir, "config.toml") + " to a dir of this session's own"}
		}
	}
	return nil
}

// needProject refuses when bs can't tell the project. A recorded session always names its
// own (OQ-K6), so this is the keeper's.
func (s *settings) needProject() error {
	if s.Project == "" {
		return &core.Exit{Code: core.ExitRefused, Msg: "no project: " + bsname.Cmd + " isn't in a git repo and BS_PROJECT is unset", Next: "cd into the project's repo"}
	}
	return nil
}

// realLogbookDirs are where the real logbooks may be: bs's default, ~/.local/share/bees
// (internal/config), and $XDG_DATA_HOME/bees.
func realLogbookDirs(env []string) []string {
	var out []string
	if h := core.Getenv(env, "HOME"); h != "" {
		out = append(out, filepath.Join(h, ".local", "share", "bees"))
	}
	if d := core.Getenv(env, "XDG_DATA_HOME"); d != "" {
		out = append(out, filepath.Join(d, "bees"))
	}
	return out
}

// samePath reports whether a and b name one directory, through symlinks.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// model is harness's model for a worker, or with queen the queen's.
func (s *settings) model(harness string, queen bool) string {
	cc, a := s.Worker.ClaudeCode, s.Worker.Agy
	if queen {
		cc, a = s.Queen.ClaudeCode, s.Queen.Agy
	}
	if harness == agy.Name {
		return a.Model
	}
	return cc.Model
}

// opts is what config sets on harness's adapter, for a worker or with queen the queen.
func (s *settings) opts(harness string, queen bool) HarnessOpts {
	return HarnessOpts{Model: s.model(harness, queen), Settings: s.Expand(s.Agy.Settings), Bin: s.program(harness)}
}

// program is the program a config harness name starts: for agy, agy.bin (bees-14.1).
func (s *settings) program(name string) string {
	if name == agy.Name {
		return s.Expand(s.Agy.Bin)
	}
	return cmp.Or(map[string]string{"claude_code": "claude"}[name], name)
}

// unpinned refuses a command that records a session (bs queen, bs spawn) under a
// BS_LOGBOOK_DIR pin other than paths.logbook_dir: the entry would land where no identity
// scan looks, since a recorded session's bs reads only the config's dir (motif 14).
func (s *settings) unpinned(cmd string) error {
	if s.PinnedDir == "" {
		return nil
	}
	return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("BS_LOGBOOK_DIR=%s isn't paths.logbook_dir (%s): a session recorded there is never found by its own "+bsname.Cmd+" calls; nothing was recorded", s.PinnedDir, s.ConfigDir),
		Next: "env -u BS_LOGBOOK_DIR " + bsname.Cmd + " " + cmd + " ..., or set paths.logbook_dir in the session's config"}
}

// command is [workspace.command] with its dir's ~ expanded; nil when unset.
func (s *settings) command() *config.Command {
	if s.Workspace.Command == nil {
		return nil
	}
	c := *s.Workspace.Command
	c.Dir = s.Expand(c.Dir)
	return &c
}

// goals is the custom goal types with each fragment's ~ expanded.
func (s *settings) goals() map[string]config.GoalType {
	out := map[string]config.GoalType{}
	for name, g := range s.Goal {
		g.Fragment = s.Expand(g.Fragment)
		out[name] = g
	}
	return out
}

// workerPath is worker.path with ~ expanded.
func (s *settings) workerPath() []string {
	var out []string
	for _, d := range s.Worker.Path {
		out = append(out, s.Expand(d))
	}
	return out
}

// hostPrompt is review.fragment, expanded: "" when unset.
func (s *settings) hostPrompt() string {
	if s.Review.Fragment == "" {
		return ""
	}
	return s.Expand(s.Review.Fragment)
}

// unmade says why spawn won't make a workspace of kind yet, "" for one it makes: until
// bees-13.10 it makes no none workspace. Spawn refuses on it, doctor fails on it and
// config init won't write it.
func unmade(kind string) string {
	if kind == config.NoWorkspace {
		return bsname.Cmd + " spawn makes git_worktree and command workspaces, and none only once bees-13.10 brings it"
	}
	return ""
}

// resolve finds who runs bs (core.Resolve): in the project of the repo bs runs in, then
// in every logbook of the config in effect and of the default config (R9), unpinned.
func (s *settings) resolve(ctx context.Context, env Env, ports Ports, dir, home string) error {
	getenv := func(k string) string { return core.Getenv(env.Vars, k) }
	var dirs []string
	for _, d := range []string{dir, ""} {
		if d == "" { // the default config: a toy's worker is spawned in the real dir
			var err error
			if d, _, err = config.Dir(func(k string) string { return map[bool]string{true: "", false: getenv(k)}[k == "HERDR_SESSION"] }); err != nil {
				return err
			}
		}
		l, err := config.Load(config.Input{Dir: d, Project: s.Project, Home: home})
		if err != nil {
			return cantRead(err)
		}
		if ld := l.Expand(l.Paths.LogbookDir); !slices.ContainsFunc(dirs, func(o string) bool { return samePath(o, ld) }) {
			dirs = append(dirs, ld)
		}
	}
	in := core.ResolveIn{Project: s.Project, Dirs: dirs, Self: env.Self, Session: s.Session, Pane: getenv("HERDR_PANE_ID"),
		Run: ports.Run, Proc: ports.Proc, Harness: ports.recorded}
	if s.Project != "" {
		in.Logbook = filepath.Join(dirs[0], s.Project+".db")
	}
	c, err := core.Resolve(ctx, in)
	s.Caller = c
	return err
}
