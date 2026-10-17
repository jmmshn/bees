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
	ErrNoRepos    = errors.New("can't find the project's repo; run bs inside it, or set BS_REPOS to the directory that holds your repos")
	ErrNoSession  = errors.New("can't tell which session you are; run bs from inside a named Claude session, or pass --as")
)

// validName is what a project name must be once lowercased with - and _ dropped. With no "-" in
// the project, the first "-" in a bead id or worker name always ends the project (R27).
var validName = regexp.MustCompile(`^[a-z][a-z0-9]{0,24}$`)

// Project is the scope of one queen, its workers and its logbook.
type Project struct {
	Name    string // e.g. "bees"
	Repo    string // main checkout of the project's git repo; empty if unknown
	Logbook string // path to this project's logbook file
}

// Queen is the queen's session name. It carries the project because Claude's and
// herdr's session names are machine-wide.
func (p Project) Queen() string { return "queen-" + p.Name }

// NeedRepo fails with ErrNoRepos when the project's repo is unknown.
func (p Project) NeedRepo() error {
	if p.Repo == "" {
		return fmt.Errorf("project %s: %w", p.Name, ErrNoRepos)
	}
	return nil
}

// Resolve finds the project from flag, then BS_PROJECT, then the git repo containing
// the working directory. A linked worktree resolves to its main repo. The repo is the
// git repo when it is the project's, else the project's directory under BS_REPOS, else
// empty: messages need only the logbook, and NeedRepo stops what would run in no repo.
func Resolve(flag string) (Project, error) {
	name, git := flag, mainRepo()
	if name == "" {
		name = os.Getenv("BS_PROJECT")
	}
	if name == "" {
		if git == "" {
			return Project{}, ErrNoProject
		}
		name = filepath.Base(git)
	}
	name = normalise(name)
	if !validName.MatchString(name) {
		return Project{}, fmt.Errorf("%q: %w", name, ErrBadProject)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Project{}, err
	}
	repo := ""
	if git != "" && normalise(filepath.Base(git)) == name {
		repo = git
	} else if repos := os.Getenv("BS_REPOS"); repos != "" {
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

// Me returns the session running bs: flag, then the name of the nearest ancestor
// process that is a registered Claude Code session, then BS_SESSION (for use outside
// Claude). The registry beats the environment, which a session can inherit stale.
func Me(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		reg := filepath.Join(home, ".claude", "sessions")
		for pid := os.Getppid(); pid > 1; pid = parentOf(pid) {
			b, err := os.ReadFile(filepath.Join(reg, fmt.Sprintf("%d.json", pid)))
			if err != nil {
				continue
			}
			var s struct{ Name string }
			if json.Unmarshal(b, &s) == nil && s.Name != "" {
				return s.Name, nil
			}
			break // the nearest Claude session has no usable name
		}
	}
	if s := os.Getenv("BS_SESSION"); s != "" {
		return s, nil
	}
	return "", ErrNoSession
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
