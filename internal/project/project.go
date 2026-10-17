// Package project resolves the scope every bs command runs in: which project,
// its logbook and repo, and which session is running bs (ADR-0006).
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrNoProject  = errors.New("can't tell which project; run inside its git repo, set BS_PROJECT or pass --project")
	ErrBadProject = errors.New("project names must be a letter then up to 24 letters or digits (after dropping - and _); set BS_PROJECT")
	ErrNoSession  = errors.New("can't tell which session you are; run bs from inside a named Claude session, or pass --as")
	ErrBadSession = errors.New("not a session name: a lowercase letter, then up to 31 of a-z, 0-9, - and _")
	ErrNotYou     = errors.New("this Claude session is registered as")
)

// validName is what a project name must be once lowercased with - and _ dropped. With no "-" in
// the project, the first "-" in a bead id or worker name always ends the project (R27).
var validName = regexp.MustCompile(`^[a-z][a-z0-9]{0,24}$`)

// Project is the scope of one queen, its workers and its logbook.
type Project struct {
	Name    string // e.g. "bees"
	Repo    string // main checkout of the project's git repo
	Logbook string // path to this project's logbook file
}

// Queen is the queen's session name. It carries the project because Claude's and
// herdr's session names are machine-wide.
func (p Project) Queen() string { return "queen-" + p.Name }

// Resolve finds the project from flag, then BS_PROJECT, then the git repo containing
// the working directory. A linked worktree resolves to its main repo.
func Resolve(flag string) (Project, error) {
	name, repo := flag, ""
	if name == "" {
		name = os.Getenv("BS_PROJECT")
	}
	if name == "" {
		repo = mainRepo()
		if repo == "" {
			return Project{}, ErrNoProject
		}
		name = filepath.Base(repo)
	}
	name = normalise(name)
	if !validName.MatchString(name) {
		return Project{}, fmt.Errorf("%q: %w", name, ErrBadProject)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Project{}, err
	}
	if repo == "" {
		repos := os.Getenv("BS_REPOS")
		if repos == "" {
			repos = filepath.Join(home, "repos", "jmmshn") // ponytail: the author's layout; set BS_REPOS elsewhere
		}
		repo = repoDir(repos, name)
	}
	lb := os.Getenv("BS_LOGBOOK")
	if lb == "" {
		lb = filepath.Join(home, ".local", "share", "bees", name+".db")
	}
	return Project{Name: name, Repo: repo, Logbook: lb}, nil
}

// normalise lowercases a project or directory name and drops - and _ (R27⁶).
func normalise(s string) string {
	return strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(s))
}

// repoDir returns the directory under repos whose normalised name is name, so project
// jobflowray finds repos/jobflow-ray; repos/<name> if none matches.
func repoDir(repos, name string) string {
	entries, _ := os.ReadDir(repos)
	for _, e := range entries {
		if e.IsDir() && normalise(e.Name()) == name {
			return filepath.Join(repos, e.Name())
		}
	}
	return filepath.Join(repos, name)
}

// mainRepo returns the main checkout of the git repo containing the working directory,
// or "" outside git. Git's common dir is shared by every worktree of a repo.
// ponytail: assumes a non-bare repo whose common dir is <checkout>/.git.
func mainRepo() string {
	out, err := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	return filepath.Dir(strings.TrimSpace(string(out)))
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
func Me(flag string) (string, error) {
	me, inClaude := registered()
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

// registered returns the name of the nearest ancestor process that is a registered,
// named Claude Code session.
func registered() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	reg := filepath.Join(home, ".claude", "sessions")
	for pid := os.Getppid(); pid > 1; pid = parentOf(pid) {
		b, err := os.ReadFile(filepath.Join(reg, fmt.Sprintf("%d.json", pid)))
		if err != nil {
			continue
		}
		var s struct{ Name string }
		if json.Unmarshal(b, &s) == nil && s.Name != "" {
			return s.Name, true
		}
		break // the nearest Claude session has no usable name
	}
	return "", false
}

// parentOf returns pid's parent from /proc, or 0 if it can't be read.
func parentOf(pid int) int {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// The command name is in parentheses and may contain spaces; fields resume after ')'.
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return 0
	}
	fields := strings.Fields(string(b[i+1:]))
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}
