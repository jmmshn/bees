// Package git is the default workspace VCS: git, with a workspace's commits read from its
// HEAD reflog.
package git

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// VCS is git, run on Run.
type VCS struct{ Run port.Runner }

var _ port.VCS = VCS{}

func (v VCS) git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := v.Run.Run(ctx, dir, append([]string{"git", "-C", dir}, args...)...)
	return strings.TrimSpace(string(out)), err
}

func exited(err error, code int) bool {
	var ee *port.ExitError
	return errors.As(err, &ee) && ee.Code == code
}

// writesCommit is a HEAD reflog subject that wrote a commit: commit (amend, initial,
// merge…), a rebase's pick, squash, fixup, reword or continue, a cherry-pick, revert or am,
// and a merge that made a merge commit. Never checkout, reset, pull or a fast-forward:
// those move HEAD to commits made elsewhere (ADR-0010).
var writesCommit = regexp.MustCompile(`^(commit( \([^)]*\))?|rebase( -i)? \((pick|squash|fixup|reword|continue)\)|cherry-pick|revert|am): |^merge [^:]*: Merge made by `)

// Commits reads dir's HEAD reflog (a worktree's own) for the entries since since that
// wrote a commit.
func (v VCS) Commits(ctx context.Context, dir string, since time.Time) ([]string, error) {
	out, err := v.git(ctx, dir, "log", "--walk-reflogs", "--date=unix", "--format=%H%x1f%gd%x1f%gs", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("reading %s's HEAD reflog: %w", dir, err)
	}
	var wc []string
	for line := range strings.Lines(out) {
		f := strings.Split(strings.TrimRight(line, "\n"), "\x1f")
		if len(f) != 3 || !writesCommit.MatchString(f[2]) {
			continue
		}
		at, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(f[1], "HEAD@{"), "}"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("reflog entry %q: no time", f[1])
		}
		if at >= since.Unix() && !slices.Contains(wc, f[0]) {
			wc = append(wc, f[0])
		}
	}
	return wc, nil
}

func (v VCS) Branch(ctx context.Context, dir, name string) (string, bool, error) {
	tip, err := v.git(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	if exited(err, 1) {
		return "", false, nil
	}
	return tip, err == nil, err
}

func (v VCS) Has(ctx context.Context, dir, commit string) (bool, error) {
	_, err := v.git(ctx, dir, "cat-file", "-e", commit+"^{commit}")
	if exited(err, 1) || exited(err, 128) {
		return false, nil
	}
	return err == nil, err
}

func (v VCS) Descends(ctx context.Context, dir, ancestor, commit string) (bool, error) {
	_, err := v.git(ctx, dir, "merge-base", "--is-ancestor", ancestor, commit)
	if exited(err, 1) {
		return false, nil
	}
	return err == nil, err
}

// Detach adds dir as a worktree of repo, detached at commit.
func (v VCS) Detach(ctx context.Context, repo, dir, commit string) error {
	_, err := v.git(ctx, repo, "worktree", "add", "--quiet", "--detach", dir, commit)
	return err
}

// Drop removes the worktree dir of repo, whatever it holds.
func (v VCS) Drop(ctx context.Context, repo, dir string) error {
	_, err := v.git(ctx, repo, "worktree", "remove", "--force", dir)
	return err
}
