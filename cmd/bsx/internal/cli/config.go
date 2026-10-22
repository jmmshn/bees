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
	GitTop       bool   // Repo is a git checkout's top level
	Logbook      string // the logbook file
	Worktrees    string // paths.worktrees with {repo} and ~ resolved
	Warnings     []string
}

// envKeys are the BS_* variables bs reads outside a herdr pane. Inside one it reads none:
// a pane's identity and config come from the pane (ADR-0010), and an inherited pin from
// some other session's env is exactly what bees-12.47 and bees-13.1's probe saw leak.
var envKeys = []string{"BS_PROJECT", "BS_LOGBOOK_DIR", "BS_REPOS"}

// load works out env's settings: its config dir (per herdr session), its project (the
// git repo bs runs in, else BS_PROJECT outside a pane), the files, the repo and the
// logbook. It fails only on what no command can run past: a broken file, a refused dir.
func load(ctx context.Context, env Env, run port.Runner) (*settings, error) {
	dir, session, err := config.Dir(env.Vars)
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
	inPane := core.Getenv(env.Vars, "HERDR_PANE_ID") != ""
	overrides := map[string]string{}
	for _, k := range envKeys {
		v := core.Getenv(env.Vars, k)
		switch {
		case v == "":
		case inPane:
			s.Warnings = append(s.Warnings, fmt.Sprintf("ignoring %s inside a herdr pane: the pane decides", k))
		default:
			overrides[k] = v
		}
	}
	if err := s.findProject(ctx, env.Dir, run, overrides["BS_PROJECT"]); err != nil {
		return nil, err
	}
	home := core.Getenv(env.Vars, "HOME")
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
		if main, err := mainCheckout(ctx, run, cwd); err == nil {
			s.Project, s.ProjectFrom, s.Repo, s.RepoFrom = normalise(filepath.Base(main)), "the git repo at "+main, main, "the git repo bs runs in"
		}
	}
	switch p := normalise(pinned); {
	case p == "":
	case s.Project == "":
		s.Project, s.ProjectFrom = p, "env BS_PROJECT"
	case p != s.Project:
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("BS_PROJECT=%s names another project than the repo you are in (%s)", pinned, s.Project),
			Next: "env -u BS_PROJECT bsx ..., or run it from " + p + "'s repo"}
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
			if e.IsDir() && normalise(e.Name()) == s.Project {
				s.Repo, s.RepoFrom = filepath.Join(dir, e.Name()), "paths.repos, "+s.Sources["paths.repos"].String()
				break
			}
		}
	}
	if s.Repo != "" {
		top, err := run.Run(ctx, s.Repo, "git", "-C", s.Repo, "rev-parse", "--show-toplevel")
		s.GitTop = err == nil && samePath(strings.TrimSpace(string(top)), s.Repo)
	}
	return nil
}

// mainCheckout is the main checkout of the git repo holding dir: a linked worktree names
// its main repo.
func mainCheckout(ctx context.Context, run port.Runner, dir string) (string, error) {
	out, err := run.Run(ctx, dir, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
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

// normalise is a repo's project name: lowercased, - and _ dropped.
func normalise(repo string) string {
	return strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(repo))
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
