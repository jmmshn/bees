// Package git is the default workspace VCS: git.
package git

import (
	"context"
	"errors"
	"slices"
	"strings"

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

var _ port.Merges = VCS{}

// CleanMerge: git rev-list --parents names a merge's two parents, and git merge-tree
// --write-tree writes the tree their clean merge gives (exit 1 on a conflict), which
// commit's own tree must be.
func (v VCS) CleanMerge(ctx context.Context, dir, commit string) (string, string, bool, error) {
	out, err := v.git(ctx, dir, "rev-list", "--parents", "-n1", commit)
	if err != nil {
		return "", "", false, err
	}
	ps := strings.Fields(out)
	if len(ps) != 3 {
		return "", "", false, nil
	}
	merged, err := v.git(ctx, dir, "merge-tree", "--write-tree", ps[1], ps[2])
	if exited(err, 1) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	tree, err := v.git(ctx, dir, "rev-parse", commit+"^{tree}")
	if err != nil {
		return "", "", false, err
	}
	merged, _, _ = strings.Cut(merged, "\n")
	return ps[1], ps[2], tree == merged, nil
}
