package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmmshn/bees/cmd/bsx/internal/config"
	"github.com/jmmshn/bees/cmd/bsx/internal/core"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// ErrDefaultConfig is bsx run where the shared bs runs: until the swap (bees-13.9) bsx
// works only inside a herdr --session toy, on that session's config and logbooks.
var ErrDefaultConfig = errors.New("bsx refuses the default config dir until the swap (bees-13.9)")

// errNoConfigDir is a session whose config dir is missing (Rule G): writing commands
// refuse; status, config and doctor run on the defaults, warning.
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
	GitTop       bool       // Repo is a git checkout's top level
	Logbook      string     // the logbook file
	Worktrees    string     // paths.worktrees with {repo} and ~ resolved
	Pane         *core.Pane // the recorded pane bs runs in; nil outside one
	Warnings     []string
}

// envKeys are the BS_* variables bs reads outside a recorded herdr pane. Inside one it
// reads none: a pane's identity and config come from the pane (ADR-0010), and an
// inherited pin from some other session's env is exactly what bees-12.47 and bees-13.1's
// probe saw leak.
var envKeys = []string{"BS_PROJECT", "BS_LOGBOOK_DIR", "BS_REPOS"}

// load works out env's settings: its config dir (per herdr session), its project (the
// logbook whose entry recorded the herdr pane bs runs in; outside a recorded pane, the
// git repo bs runs in, else BS_PROJECT), the files, the repo and the logbook. It fails
// only on what no command can run past: a broken file, a refused dir, a pane bs can't
// tell the session of.
func load(ctx context.Context, env Env, ports Ports) (*settings, error) {
	run := ports.Run
	dir, session, err := config.Dir(func(k string) string { return core.Getenv(env.Vars, k) })
	if err != nil {
		return nil, err
	}
	if session == config.DefaultSession {
		return nil, fmt.Errorf("%w: run it on a herdr --session server, whose config lives in %s",
			ErrDefaultConfig, filepath.Join(dir, "sessions", "<session>"))
	}
	s := &settings{Dir: dir, Session: session}
	if fi, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		s.Missing = true
		s.Warnings = append(s.Warnings, fmt.Sprintf("%v %s: %s is missing, so only status, config and doctor run, on the defaults\n  next: mkdir -p %s and write its config.toml (the live-test skill's setup.sh does)",
			errNoConfigDir, session, dir, dir))
	} else if err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("config dir %s is not a directory", dir)
	}
	home := core.Getenv(env.Vars, "HOME")
	if pane := core.Getenv(env.Vars, "HERDR_PANE_ID"); pane != "" {
		global, err := config.Load(config.Input{Dir: dir, Home: home})
		if err != nil {
			return nil, err
		}
		p, ok, err := core.ScanPanes(ctx, core.ScanIn{Dir: global.Expand(global.Paths.LogbookDir), Session: session, Pane: pane,
			Self: env.Self, Run: run, Proc: ports.Proc, Kind: harnessProgram(global.Harness)})
		if err != nil {
			return nil, err
		}
		if ok {
			s.Pane = &p
		}
	}
	inPane := s.Pane != nil
	overrides := map[string]string{}
	for _, k := range envKeys {
		v := core.Getenv(env.Vars, k)
		switch {
		case v == "":
		case inPane:
			s.Warnings = append(s.Warnings, fmt.Sprintf("ignoring %s inside %s's recorded herdr pane: the pane decides", k, s.Pane.Who.Name))
		default:
			overrides[k] = v
		}
	}
	if inPane {
		s.Project, s.ProjectFrom = s.Pane.Project, fmt.Sprintf("herdr pane %s, recorded for %s in %s", s.Pane.Pane, s.Pane.Who.Name, s.Pane.Logbook)
		if s.Pane.Repo != "" {
			s.Repo, s.RepoFrom = s.Pane.Repo, "the workspace "+s.Pane.Who.Name+"'s spawn recorded"
		} else if err := s.cwdRepo(ctx, env.Dir, run); err != nil {
			return nil, err
		}
	} else if err := s.findProject(ctx, env.Dir, run, overrides["BS_PROJECT"]); err != nil {
		return nil, err
	}
	l, err := config.Load(config.Input{Dir: dir, Project: s.Project, Home: home, Env: overrides})
	if err != nil {
		return nil, err
	}
	s.Loaded = l
	if err := s.findRepo(ctx, run); err != nil {
		return nil, err
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
	if cwd != "" {
		main, err := mainCheckout(ctx, run, cwd)
		switch {
		case errors.Is(err, errNotGit):
		case err != nil: // "couldn't look" is never "no project" (Rule G)
			return &core.Exit{Code: core.ExitCantCheck, Msg: "bs can't tell which project " + cwd + " is in", Err: err}
		default:
			s.Project, s.ProjectFrom, s.Repo, s.RepoFrom = config.Normalise(filepath.Base(main)), "the git repo at "+main, main, "the git repo bs runs in"
		}
	}
	switch p := config.Normalise(pinned); {
	case p == "":
	case s.Project == "":
		s.Project, s.ProjectFrom = p, "env BS_PROJECT"
	case p != s.Project:
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("BS_PROJECT=%s names another project than the repo you are in (%s)", pinned, s.Project),
			Next: "env -u BS_PROJECT bsx ..., or run it from " + p + "'s repo"}
	}
	return nil
}

// cwdRepo takes the git repo bs runs in as the repo when it is the pane's project's: a
// queen's entry records no repo.
func (s *settings) cwdRepo(ctx context.Context, cwd string, run port.Runner) error {
	if cwd == "" {
		return nil
	}
	main, err := mainCheckout(ctx, run, cwd)
	switch {
	case errors.Is(err, errNotGit):
	case err != nil:
		return &core.Exit{Code: core.ExitCantCheck, Msg: "bs can't tell which project " + cwd + " is in", Err: err}
	case config.Normalise(filepath.Base(main)) == s.Project:
		s.Repo, s.RepoFrom = main, "the git repo bs runs in"
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
			return fmt.Errorf("paths.repos %s: %w", dir, err)
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
	top, err := run.Run(ctx, s.Repo, "git", "-C", s.Repo, "rev-parse", "--show-toplevel")
	switch err := notGit(err); {
	case errors.Is(err, errNotGit):
	case err != nil: // a failing git must not quietly make the kind none
		return &core.Exit{Code: core.ExitCantCheck, Msg: "bs can't tell whether " + s.Repo + " is a git checkout", Err: err}
	default:
		s.GitTop = samePath(strings.TrimSpace(string(top)), s.Repo)
	}
	return nil
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

// mainCheckout is the main checkout of the git repo holding dir: a linked worktree names
// its main repo.
func mainCheckout(ctx context.Context, run port.Runner, dir string) (string, error) {
	out, err := run.Run(ctx, dir, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", notGit(err)
	}
	common := strings.TrimSpace(string(out))
	if filepath.Base(common) != ".git" {
		return "", fmt.Errorf("%s: a bare repo", common)
	}
	return filepath.Dir(common), nil
}

// checkLogbook checks s names a project and that its logbook isn't the real one, which no toy may migrate before the swap.
func (s *settings) checkLogbook(env Env) error {
	if s.Project == "" {
		return &core.Exit{Code: core.ExitRefused, Msg: "no project: bs isn't in a git repo and BS_PROJECT is unset",
			Next: "cd into the project's repo"}
	}
	for _, real := range realLogbookDirs(env.Vars) {
		if samePath(filepath.Dir(s.Logbook), real) {
			return fmt.Errorf("%w: paths.logbook_dir is the real logbooks' dir, %s (%s)", ErrDefaultConfig,
				s.Paths.LogbookDir, s.Sources["paths.logbook_dir"])
		}
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

// harnessProgram is the program a config harness name starts.
func harnessProgram(name string) string {
	return cmp.Or(map[string]string{"claude_code": "claude"}[name], name)
}
