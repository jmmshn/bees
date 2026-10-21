package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/project"
)

// ownModule is bs's module: a directory whose go.mod declares it is bs's source.
const ownModule = "github.com/jmmshn/bees"

// buildRevision is the commit bs was built from, from Go's VCS stamp (go install in
// the repo sets it), or "" if there is none. Tests swap it out.
var buildRevision = func() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return ""
}

func cmdVersion(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	rev := buildRevision()
	if rev == "" {
		rev = "unknown (built without a VCS stamp)"
	}
	_, err := fmt.Fprintf(stdout, "bs %s\n", rev)
	return err
}

// executable is the path of the running bs. Tests swap it out.
var executable = os.Executable

// warnIfStale warns when bs runs inside its own source repo but was built from
// something other than what that repo says it should run (AGENTS.md motif 14):
//
//   - a worktree build (the binary lives in the checkout, e.g. bin/bs) must match the
//     checkout's HEAD;
//   - the shared install must be built from main: not behind it (merged work never
//     installed) and not from a commit main lacks (a worker's branch build).
//
// The shared bs is the queen's to install, so a worker is told to ask the queen. It
// stays quiet whenever it can't tell: no stamp, not bs's repo, or a commit the repo
// doesn't have.
//
// ponytail: compares with the local main (not origin/main), ignores vcs.modified, and
// reads only go.mod's first line; enough to catch "merged but never installed".
func warnIfStale(ctx context.Context, stderr io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second) // never hold up the command
	defer cancel()
	rev := buildRevision()
	repo := sourceRepo()
	if rev == "" || repo == "" {
		return
	}
	git := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := git("cat-file", "-e", rev+"^{commit}"); err != nil {
		return
	}
	if exe, err := executable(); err == nil && strings.HasPrefix(exe, repo+string(filepath.Separator)) {
		if head, err := git("rev-parse", "HEAD"); err == nil && head != rev {
			rel, _ := filepath.Rel(repo, exe) // exe is under repo
			fmt.Fprintf(stderr, "bs: this bs was built from %.12s, not this checkout's HEAD %.12s; rebuild it: go build -o %s ./cmd/bs\n", rev, head, rel)
		}
		return
	}
	var what string
	if _, err := git("merge-base", "--is-ancestor", rev, "main"); err != nil {
		what = "a commit not on main (a branch build)"
	} else if n, err := git("rev-list", "--count", rev+"..main"); err != nil || n == "0" {
		return
	} else if n == "1" {
		what = "1 commit behind main"
	} else {
		what = n + " commits behind main"
	}
	fmt.Fprintf(stderr, "bs: this bs was built from %.12s, %s; %s\n", rev, what, staleFix())
}

// staleFix says who rebuilds the shared bs: a worker asks its queen; the queen or the
// keeper installs it from main.
func staleFix() string {
	if name, err := project.Name(""); err == nil {
		queen := project.Project{Name: name}.Queen()
		if me, err := project.Me(""); err == nil && me != queen {
			return fmt.Sprintf("don't install it yourself; tell the queen: bs send %s \"bs is stale\"", queen)
		}
	}
	return "rebuild it from main: go install ./cmd/bs"
}

// sourceRepo returns the directory of the nearest go.mod above the working directory
// if it declares ownModule, else "".
func sourceRepo() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if f, err := os.Open(filepath.Join(dir, "go.mod")); err == nil {
			sc := bufio.NewScanner(f)
			ok := sc.Scan() && strings.TrimSpace(sc.Text()) == "module "+ownModule
			f.Close()
			if ok {
				return dir
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
