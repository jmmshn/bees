// Package project resolves the scope every pns command runs in: which project,
// its logbook and repo, and which session is running pns (ADR-0006).
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	ErrNoProject = errors.New("can't tell which project; run inside its git repo, set PNS_PROJECT or pass --project")
	ErrNoSession = errors.New("can't tell which session you are; run pns from inside a named Claude session, or pass --as")
)

// Project is the scope of one chief, its peons and its logbook.
type Project struct {
	Name    string // e.g. "peons"
	Repo    string // main checkout of the project's git repo
	Logbook string // path to this project's logbook file
}

// Chief is the chief's session name. It carries the project because Claude's and
// herdr's session names are machine-wide.
func (p Project) Chief() string { return "chief-" + p.Name }

// Resolve finds the project from flag, then PNS_PROJECT, then the git repo containing
// the working directory. A linked worktree resolves to its main repo.
func Resolve(flag string) (Project, error) {
	name, repo := flag, ""
	if name == "" {
		name = os.Getenv("PNS_PROJECT")
	}
	if name == "" {
		repo = mainRepo()
		if repo == "" {
			return Project{}, ErrNoProject
		}
		name = filepath.Base(repo)
	}
	if repo == "" {
		repos := os.Getenv("PNS_REPOS")
		if repos == "" {
			repos = filepath.Join(os.Getenv("HOME"), "repos", "jmmshn")
		}
		repo = filepath.Join(repos, name)
	}
	lb := os.Getenv("PNS_LOGBOOK")
	if lb == "" {
		lb = filepath.Join(os.Getenv("HOME"), ".local", "share", "peons", name+".db")
	}
	return Project{Name: name, Repo: repo, Logbook: lb}, nil
}

// mainRepo returns the main checkout of the git repo containing the working directory,
// or "" outside git. Git's common dir is shared by every worktree of a repo.
func mainRepo() string {
	out, err := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	return filepath.Dir(strings.TrimSpace(string(out)))
}

// Me returns the session running pns: flag, then PNS_SESSION, then the name of the
// nearest ancestor process that is a registered Claude Code session.
func Me(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if s := os.Getenv("PNS_SESSION"); s != "" {
		return s, nil
	}
	reg := filepath.Join(os.Getenv("HOME"), ".claude", "sessions")
	for pid := os.Getppid(); pid > 1; pid = parentOf(pid) {
		b, err := os.ReadFile(filepath.Join(reg, fmt.Sprintf("%d.json", pid)))
		if err != nil {
			continue
		}
		var s struct{ Name string }
		if json.Unmarshal(b, &s) == nil && s.Name != "" {
			return s.Name, nil
		}
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
	fields := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}
