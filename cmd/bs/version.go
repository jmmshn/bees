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

// warnIfStale warns when bs runs inside its own source repo but was built from a
// commit behind that repo's main: merged work not installed yet (AGENTS.md motif 14).
// It stays quiet whenever it can't tell: no stamp, not bs's repo, or a build from a
// commit main doesn't contain (a branch build).
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
	if _, err := git("merge-base", "--is-ancestor", rev, "main"); err != nil {
		return
	}
	n, err := git("rev-list", "--count", rev+"..main")
	if err != nil || n == "0" {
		return
	}
	plural := "s"
	if n == "1" {
		plural = ""
	}
	fmt.Fprintf(stderr, "bs: this bs was built from %.12s, %s commit%s behind main; rebuild it: go install ./cmd/bs\n", rev, n, plural)
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
