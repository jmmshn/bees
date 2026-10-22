package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/port"
)

// ownModule is bs's module: a directory whose go.mod declares it is bs's source.
const ownModule = "github.com/jmmshn/bees"

// version prints the commit bs was built from.
func (r *run) version(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	rev := r.ports.Revision
	if rev == "" {
		rev = "unknown (built without a VCS stamp)"
	}
	_, err := fmt.Fprintf(r.out, "%s %s\n", bsname.Cmd, rev)
	return err
}

// warnIfStale warns when bs runs inside its own source repo but was built from something
// other than what that repo says it should run (motif 14):
//
//   - a worktree build (the binary lives in the checkout, e.g. bin/bs) must match the
//     checkout's HEAD;
//   - the shared install must be built from main: not behind it (merged work never
//     installed) and not from a commit main lacks (a worker's branch build).
//
// The shared bs is the queen's to install, so a worker is told to ask the queen. It stays
// quiet whenever it can't tell: no stamp, not bs's repo, or a commit the repo lacks.
//
// ponytail: compares with the local main (not origin/main), ignores vcs.modified, and reads
// only go.mod's first line; enough to catch "merged but never installed".
func warnIfStale(ctx context.Context, env Env, ports Ports, s *settings, stderr io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second) // never hold up the command
	defer cancel()
	rev, repo := ports.Revision, sourceRepo(env.Dir)
	if rev == "" || repo == "" || ports.Run == nil {
		return
	}
	git := func(args ...string) (string, error) {
		out, err := ports.Run.Run(ctx, repo, append([]string{"git"}, args...)...)
		return strings.TrimSpace(string(out)), err
	}
	if _, err := git("cat-file", "-e", rev+"^{commit}"); err != nil {
		return
	}
	if exe := ports.Exe; strings.HasPrefix(exe, repo+string(filepath.Separator)) {
		if head, err := git("rev-parse", "HEAD"); err == nil && head != rev {
			rel, _ := filepath.Rel(repo, exe) // exe is under repo
			fmt.Fprintf(stderr, "%s: this bs was built from %.12s, not this checkout's HEAD %.12s; rebuild it: go build -o %s ./cmd/bs\n", bsname.Cmd, rev, head, rel)
		}
		return
	}
	var what string
	var ee *port.ExitError
	if _, err := git("merge-base", "--is-ancestor", rev, "main"); errors.As(err, &ee) && ee.Code == 1 {
		what = "a commit not on main (a branch build)"
	} else if err != nil { // no main, a timeout: it can't tell
		return
	} else if n, err := git("rev-list", "--count", rev+"..main"); err != nil || n == "0" {
		return
	} else if n == "1" {
		what = "1 commit behind main"
	} else {
		what = n + " commits behind main"
	}
	fmt.Fprintf(stderr, "%s: this bs was built from %.12s, %s; %s\n", bsname.Cmd, rev, what, staleFix(s))
}

// staleFix says who rebuilds the shared bs: a worker asks its queen; the queen or the
// keeper installs it from main.
func staleFix(s *settings) string {
	if s != nil && s.Pane != nil && !s.Pane.Who.Queen {
		return fmt.Sprintf("don't install it yourself; tell the queen: %s send queen-%s \"bs is stale\"", bsname.Cmd, s.Project)
	}
	return "rebuild it from main: go install ./cmd/bs"
}

// sourceRepo returns the directory of the nearest go.mod at or above dir if it declares
// ownModule, else "".
func sourceRepo(dir string) string {
	for dir != "" {
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
	return ""
}
