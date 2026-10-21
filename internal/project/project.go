// Package project resolves the scope every bs command runs in: which project,
// its logbook and repo, and which session is running bs (ADR-0006).
package project

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jmmshn/bees/internal/proc"
)

var (
	ErrNoProject    = errors.New("can't tell which project; run inside its git repo, set BS_PROJECT or pass --project")
	ErrBadProject   = errors.New("project names must be a letter then up to 24 letters or digits (after dropping - and _); set BS_PROJECT")
	ErrNoRepos      = errors.New("can't find the project's repo; run bs inside it, or set BS_REPOS (or paths.repos in bs's config.toml) to the directory that holds your repos")
	ErrNoSession    = errors.New("can't tell which session you are; run bs from inside a named Claude session, or pass --as")
	ErrBadSession   = errors.New("not a session name: a lowercase letter, then up to 31 of a-z, 0-9, - and _")
	ErrNotYou       = errors.New("this Claude session is registered as")
	ErrWrongProject = errors.New("this session's BS_PROJECT names another project than the repo you are in")
)

// validName is what a project name must be once lowercased with - and _ dropped. With no "-" in
// the project, the first "-" in a bead id or worker name always ends the project (R27).
var validName = regexp.MustCompile(`^[a-z][a-z0-9]{0,24}$`)

// Project is the scope of one queen, its workers and its logbook.
type Project struct {
	Name      string // e.g. "bees"
	Repo      string // main checkout of the project's git repo; empty if unknown
	RepoFrom  string // the link of the repo chain that gave Repo: --repo, git, paths.repo or paths.repos
	Base      string // the linked worktree workers branch from; empty: Repo
	Worktrees string // the parent dir of worker worktrees; empty if Repo is
	Logbook   string // path to this project's logbook file
	repoErr   error  // why the chain's repo is unusable; NeedRepo returns it
}

// RepoPlaceholder, in paths.worktrees, stands for the main checkout of the repo a
// worker's worktree is cut from (ADR-0008).
const RepoPlaceholder = "{repo}"

// BaseRepo is the checkout workers branch from: Base, else Repo.
func (p Project) BaseRepo() string { return cmp.Or(p.Base, p.Repo) }

// Queen is the queen's session name. It carries the project because Claude's and
// herdr's session names are machine-wide.
func (p Project) Queen() string { return "queen-" + p.Name }

// NeedRepo fails when the project's repo is unusable (a --repo or paths.repo that is no
// git checkout), or with ErrNoRepos when it is unknown.
func (p Project) NeedRepo() error {
	if p.repoErr != nil {
		return p.repoErr
	}
	if p.Repo == "" {
		return fmt.Errorf("project %s: %w", p.Name, ErrNoRepos)
	}
	return nil
}

// Paths is where bs's config (ADR-0008) and --repo say a project's files live, with ~
// already expanded.
type Paths struct {
	LogbookDir string // the directory holding <project>.db
	RepoFlag   string // --repo: beats every other link of the repo chain; "" if unset
	Repo       string // paths.repo, the project file's checkout; "" if unset
	Repos      string // paths.repos, the directory holding the user's repos; "" if unset
	Worktrees  string // paths.worktrees; {repo} stands for the main checkout
}

// Name finds the project from flag, then BS_PROJECT, then the git repo containing the
// working directory; a linked worktree names its main repo. Inside a Claude session, an
// inherited BS_PROJECT holds only within the checkout the session was launched in (a
// submodule in it too): a worker that runs bs in another project's repo must pick with
// --project (bees-8.7). The name comes before any config, so no file can change it.
func Name(flag string) (string, error) {
	name, git := flag, mainRepo("")
	if name == "" {
		name = os.Getenv("BS_PROJECT")
		if name != "" && git != "" && Normalise(filepath.Base(git)) != Normalise(name) {
			if err := inLaunchCheckout(name, git); err != nil {
				return "", err
			}
		}
	}
	if name == "" {
		if git == "" {
			return "", ErrNoProject
		}
		name = filepath.Base(git)
	}
	name = Normalise(name)
	if !validName.MatchString(name) {
		return "", fmt.Errorf("%q: %w", name, ErrBadProject)
	}
	return name, nil
}

// Locate finds project name's logbook and repo. The logbook is BS_LOGBOOK, else
// <c.LogbookDir>/<name>.db. The repo is the first of the chain (ADR-0008): c.RepoFlag, the
// git repo containing the working directory when it is the project's, c.Repo, then the
// project's directory under c.Repos, else empty: messages need only the logbook, and
// NeedRepo stops what would run in no repo. A flag or c.Repo naming a linked worktree
// makes it the Base workers branch from, while Repo and Worktrees stay the main checkout's;
// one that is no git checkout fails only NeedRepo, so messages still flow.
func Locate(name string, c Paths) (Project, error) {
	p := Project{Name: name}
	git := mainRepo("")
	switch {
	case c.RepoFlag != "":
		p.Repo, p.Base, p.repoErr = checkout("--repo", c.RepoFlag)
		p.RepoFrom = "--repo"
	case git != "" && Normalise(filepath.Base(git)) == name:
		p.Repo, p.RepoFrom = git, "git"
	case c.Repo != "":
		p.Repo, p.Base, p.repoErr = checkout("paths.repo", c.Repo)
		p.RepoFrom = "paths.repo"
	case c.Repos != "":
		p.Repo, p.RepoFrom = repoDir(c.Repos, name), "paths.repos"
	}
	if p.Repo != "" {
		p.Worktrees = strings.Replace(c.Worktrees, RepoPlaceholder, p.Repo, 1)
	}
	p.Logbook = os.Getenv("BS_LOGBOOK")
	if p.Logbook == "" {
		p.Logbook = filepath.Join(c.LogbookDir, name+".db")
	}
	var err error
	p.Logbook, err = filepath.Abs(p.Logbook)
	return p, err
}

// RepoErr is why the repo chain's repo is unusable, nil if it is fine or unknown.
func (p Project) RepoErr() error { return p.repoErr }

// checkout returns the main checkout of the git repo at dir, named by what (for errors),
// and dir's own checkout when that is a linked worktree of it.
func checkout(what, dir string) (main, base string, err error) {
	if dir, err = filepath.Abs(dir); err != nil {
		return "", "", err
	}
	main, top := mainRepo(dir), topLevel(dir)
	if main == "" || top == "" {
		return "", "", fmt.Errorf("%s %s is not a git checkout", what, dir)
	}
	if top != main {
		base = top
	}
	return main, base, nil
}

// LogbookDir is the directory to hand a worker as BS_LOGBOOK_DIR, so its bs opens p's
// logbook. A logbook not named <project>.db (a BS_LOGBOOK of another name) has none.
func (p Project) LogbookDir() (string, error) {
	if filepath.Base(p.Logbook) != p.Name+".db" {
		return "", fmt.Errorf("BS_LOGBOOK=%s is not named %s.db, so a worker can't be pointed at it with BS_LOGBOOK_DIR; rename the file, or unset BS_LOGBOOK", p.Logbook, p.Name)
	}
	return filepath.Dir(p.Logbook), nil
}

// Normalise lowercases a project or directory name and drops - and _ (R27⁶).
func Normalise(s string) string {
	return strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(s))
}

// repoDir returns the directory under repos whose normalised name is name, so project
// jobflowray finds repos/jobflow-ray; repos/<name> if none matches.
func repoDir(repos, name string) string {
	entries, _ := os.ReadDir(repos)
	for _, e := range entries {
		if e.IsDir() && Normalise(e.Name()) == name {
			return filepath.Join(repos, e.Name())
		}
	}
	return filepath.Join(repos, name)
}

// inLaunchCheckout refuses an inherited BS_PROJECT name when bs runs inside a Claude
// session outside the checkout that session was launched in, git being the repo bs is in.
func inLaunchCheckout(name, git string) error {
	_, cwd, inClaude, err := registered()
	if err != nil || !inClaude {
		return err
	}
	if cwd == "" { // fail closed: without it, any repo would pass as the session's own
		return fmt.Errorf("%w: the Claude session has no recorded launch directory; pass --project", ErrWrongProject)
	}
	if mainRepo(cwd) == git {
		return nil
	}
	if top := topLevel(cwd); top != "" {
		if wd, err := os.Getwd(); err == nil {
			if rel, err := filepath.Rel(top, wd); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
				return nil // a submodule or nested repo inside the session's own checkout
			}
		}
	}
	var picks []string
	for _, n := range []string{Normalise(filepath.Base(git)), Normalise(name)} {
		if validName.MatchString(n) {
			picks = append(picks, "--project "+n)
		}
	}
	return fmt.Errorf("%w: you are in %s; pass %s", ErrWrongProject, git, cmp.Or(strings.Join(picks, " or "), "--project"))
}

// mainRepo returns the main checkout of the git repo containing dir (the working
// directory if ""), or "" outside git. Git's common dir, shared by every worktree of a
// repo, is <checkout>/.git; a submodule's lives in its superproject's .git/modules, so a
// submodule is its own checkout. ponytail: worktrees of a submodule resolve to themselves.
func mainRepo(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	if common := strings.TrimSpace(string(out)); filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return topLevel(dir)
}

// topLevel returns the top of the checkout containing dir, or "" outside git.
func topLevel(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SessionName is what a session name must be: a worker's name (worker.Name) or a
// queen's. Names reach the logbook, herdr and the doorbell Claude reads as a user turn,
// so free text never passes (bees-7.4).
var SessionName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// CheckSession refuses a session name that breaks SessionName.
func CheckSession(name string) error {
	if !SessionName.MatchString(name) {
		return fmt.Errorf("%q: %w", name, ErrBadSession)
	}
	return nil
}

// Me returns the session running bs: the name of the nearest ancestor process that is a
// registered Claude Code session, else flag, else BS_SESSION (for use outside Claude).
// Inside a Claude session the registry is the sender's identity: a flag naming anyone
// else is refused, so a session can't send as the queen (bees-7.4). The registry also
// beats the environment, which a session can inherit stale.
//
// The guard stops a careless --as, not a determined one: a bs detached from its session
// (`( bs … & )`, nohup, setsid) is reparented to init, so the walk finds no Claude
// session and --as names anyone; any process of the same user can also write the logbook
// directly. Same-uid processes can't be fenced from each other (bees-8.8).
func Me(flag string) (string, error) {
	me, _, inClaude, err := registered()
	if err != nil {
		return "", err
	}
	switch {
	case inClaude && flag != "" && flag != me:
		return "", fmt.Errorf("--as %s: %w %q", flag, ErrNotYou, me)
	case inClaude:
	case flag != "":
		me = flag
	case os.Getenv("BS_SESSION") != "":
		me = os.Getenv("BS_SESSION")
	default:
		return "", ErrNoSession
	}
	return me, CheckSession(me)
}

// registered returns the name and launch directory of the nearest ancestor process that
// is a registered, named, live Claude Code session: its registry entry's procStart must match the pid's
// start time, as in claude.List, so a stale entry on a reused pid names no one. A failed
// process lookup is an error, never "not in Claude": that would let --as name anyone.
func registered() (name, cwd string, ok bool, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", false, err
	}
	reg := filepath.Join(home, ".claude", "sessions")
	for pid := os.Getppid(); pid > 1; {
		b, err := os.ReadFile(filepath.Join(reg, fmt.Sprintf("%d.json", pid)))
		if err == nil {
			var s struct{ Name, ProcStart, Cwd string }
			if json.Unmarshal(b, &s) != nil || s.Name == "" {
				break // the nearest Claude session has no usable name
			}
			start, err := proc.Start(pid)
			if err != nil {
				return "", "", false, fmt.Errorf("checking Claude session %s (pid %d): %w", s.Name, pid, err)
			}
			if start == s.ProcStart {
				return s.Name, s.Cwd, true, nil
			}
			// A stale entry: the pid was reused by a process that isn't that session.
		}
		if pid, err = proc.Parent(pid); err != nil {
			return "", "", false, fmt.Errorf("finding the Claude session that ran bs: %w", err)
		}
	}
	return "", "", false, nil
}
