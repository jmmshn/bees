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

	"github.com/jmmshn/bees/internal/port"
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

// writesCommit is a HEAD reflog subject of a commit the worker wrote: commit, amend,
// initial, a merge commit (one that ended a conflict, or "Merge made by"), and a revert.
// Never a rebase's pick, a pull, a cherry-pick, am, checkout, reset or fast-forward: those
// move HEAD to commits, or copies of patches, written elsewhere. A rebased copy of a
// written commit is matched by its patch-id instead (PatchIDs; keeper, 2026-10-22).
var writesCommit = regexp.MustCompile(`^(commit( \((amend|initial|merge)\))?|revert): |^merge [^:]*: Merge made by `)

// Commits reads dir's HEAD reflog (a worktree's own) for the entries since since that
// wrote a commit (writesCommit).
func (v VCS) Commits(ctx context.Context, dir string, since time.Time) ([]string, error) {
	out, err := v.git(ctx, dir, "log", "--walk-reflogs", "--date=unix", "--format=%H%x1f%gd%x1f%gs", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("reading %s's HEAD reflog: %w", dir, err)
	}
	if out == "" {
		// Even a new worktree's HEAD reflog has its creation: none at all is a reflog git
		// doesn't keep (core.logAllRefUpdates), never "nothing was committed" (motif 11).
		return nil, fmt.Errorf("%s has no HEAD reflog to read its commits from", dir)
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

// Pushed is the tip of a remote-tracking branch named name, which a git push updates
// (from a detached HEAD too): name's upstream when it is one of them, else the one tip
// they share. Two remotes' copies that disagree say nothing.
func (v VCS) Pushed(ctx context.Context, dir, name string) (string, bool, error) {
	up, err := v.git(ctx, dir, "for-each-ref", "--format=%(upstream)", "refs/heads/"+name)
	if err != nil {
		return "", false, err
	}
	out, err := v.git(ctx, dir, "for-each-ref", "--format=%(objectname) %(refname)", "refs/remotes/*/"+name)
	if err != nil {
		return "", false, err
	}
	var tips []string
	for line := range strings.Lines(out) {
		tip, ref, _ := strings.Cut(strings.TrimSpace(line), " ")
		if strings.Count(ref, "/") != strings.Count(name, "/")+3 {
			continue // refs/remotes/a/b/<name>: remote a's branch b/<name>
		}
		if ref == up {
			return tip, true, nil
		}
		if !slices.Contains(tips, tip) {
			tips = append(tips, tip)
		}
	}
	if len(tips) != 1 {
		return "", false, nil
	}
	return tips[0], true, nil
}

// PatchIDs is git patch-id --stable of each of commits dir has, read in two calls: which
// of them dir has (rev-list --ignore-missing), then their patches through git patch-id. A
// merge, shown with no patch (--diff-merges=off), has none.
func (v VCS) PatchIDs(ctx context.Context, dir string, commits []string) (map[string]string, []string, error) {
	if len(commits) == 0 {
		return nil, nil, nil
	}
	out, err := v.git(ctx, dir, append([]string{"rev-list", "--no-walk=unsorted", "--ignore-missing"}, commits...)...)
	if err != nil {
		return nil, nil, err
	}
	have := strings.Fields(out)
	var missing []string
	for _, c := range commits {
		if !slices.Contains(have, c) {
			missing = append(missing, c)
		}
	}
	if len(have) == 0 {
		return nil, missing, nil
	}
	// sh only for the pipe: dir and the commits are its arguments, never its script.
	script := `set -e; p=$(git -C "$0" log --no-walk=unsorted --diff-merges=off --no-color -p "$@"); printf '%s\n' "$p" | git patch-id --stable`
	b, err := v.Run.Run(ctx, dir, append([]string{"sh", "-c", script, dir}, have...)...)
	if err != nil {
		return nil, nil, fmt.Errorf("git patch-id in %s: %w", dir, err)
	}
	ids := map[string]string{}
	for line := range strings.Lines(string(b)) {
		if id, c, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			ids[c] = id
		}
	}
	return ids, missing, nil
}

// Has: git cat-file -e says a missing object with exit 128 and "Not a valid object
// name"; any other failure is an error, never "missing".
func (v VCS) Has(ctx context.Context, dir, commit string) (bool, error) {
	_, err := v.git(ctx, dir, "cat-file", "-e", commit+"^{commit}")
	var ee *port.ExitError
	if errors.As(err, &ee) && (ee.Code == 1 || ee.Code == 128 && strings.Contains(string(ee.Stderr), "Not a valid object name")) {
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
