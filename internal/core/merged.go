package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmmshn/bees/internal/logbook"
)

// merged reports whether ws's branch is merged into its recorded base, and how bs knows or
// why not: the pilot's three proofs (origin/pilot/bees-12:internal/worker/merged.go),
// checked against the base and, when it has one, the base's upstream. A rebase or squash
// merge rewrites the commits, so ancestry is only the first proof; then every path the
// branch changed being identical on the base; then a merged change on the review host
// whose head is the branch tip. The local proofs run first, so the host is asked only
// when they fail.
// err is a proof that couldn't run: never "not merged" (Rule G).
func (c *Core) merged(ctx context.Context, ws logbook.Workspace) (bool, string, error) {
	tip, err := c.git(ctx, ws.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+ws.Branch)
	switch {
	case isExit(err, 1):
		return false, fmt.Sprintf("branch %s no longer exists in %s", ws.Branch, ws.Repo), nil
	case err != nil:
		return false, "", err
	}
	bases, err := c.bases(ctx, ws)
	if err != nil {
		return false, "", err
	}
	for _, b := range bases {
		switch in, err := c.ancestor(ctx, ws.Repo, "refs/heads/"+ws.Branch, b); {
		case err != nil:
			return false, "", err
		case in:
			return true, "its commits are on " + b, nil
		}
		switch same, err := c.sameContent(ctx, ws.Repo, b, "refs/heads/"+ws.Branch); {
		case err != nil:
			return false, "", fmt.Errorf("comparing with %s: %w", b, err)
		case same:
			return true, "the files it changed are identical on " + b + " (a squash or rebase merge)", nil
		}
	}
	switch heads, err := c.Review.Landed(ctx, ws.Repo, ws.Branch); {
	case err != nil:
		return false, "", fmt.Errorf("not merged locally, and %s couldn't be asked: %w", c.Review.Name(), err)
	case slices.Contains(heads, tip):
		return true, "a merged change on " + c.Review.Name() + " has its tip as head", nil
	}
	return false, fmt.Sprintf("%s has work not on %s", ws.Branch, strings.Join(bases, " or ")), nil
}

// base is the first of ws's bases: the ref its branch is measured against.
func (c *Core) base(ctx context.Context, ws logbook.Workspace) (string, error) {
	bs, err := c.bases(ctx, ws)
	if err != nil {
		return "", err
	}
	return bs[0], nil
}

// bases is where ws's branch lands: its recorded base and, when it has one, the base's
// upstream (where a hosted merge is pulled). A spawn from before goals recorded no base
// ref (main's v0) or the repo's path in its place (the pilot's v1), and a base branch can
// be deleted since: then its bases are the pilot's default branches, origin's default
// branch and the local one of the same name, or main.
func (c *Core) bases(ctx context.Context, ws logbook.Workspace) ([]string, error) {
	gone := false
	if ws.Base != "" && !filepath.IsAbs(ws.Base) {
		// A base deleted since (a stacked base merged and removed) lands where it landed:
		// the default branches, never "can't check" forever (motif 5).
		_, err := c.git(ctx, ws.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+ws.Base)
		switch {
		case isExit(err, 1):
			gone = true
		case err != nil:
			return nil, err
		}
	}
	if ws.Base != "" && !filepath.IsAbs(ws.Base) && !gone {
		up, err := c.git(ctx, ws.Repo, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+ws.Base)
		if err != nil || up == "" {
			return []string{ws.Base}, err
		}
		return []string{ws.Base, up}, nil
	}
	cands := []string{"main"}
	switch head, err := c.git(ctx, ws.Repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); {
	case err == nil && head != "":
		cands = []string{head, strings.TrimPrefix(head, "origin/")}
	case err != nil && !isExit(err, 1):
		return nil, err
	}
	var out []string
	for _, b := range cands {
		switch _, err := c.git(ctx, ws.Repo, "rev-parse", "--verify", "--quiet", b+"^{commit}"); {
		case err == nil:
			out = append(out, b)
		case !isExit(err, 1):
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no base recorded and no default branch (tried %s)", strings.Join(cands, ", "))
	}
	return out, nil
}

// ancestor reports whether commit a is an ancestor of b.
func (c *Core) ancestor(ctx context.Context, repo, a, b string) (bool, error) {
	_, err := c.git(ctx, repo, "merge-base", "--is-ancestor", a, b)
	if isExit(err, 1) {
		return false, nil
	}
	return err == nil, err
}

// sameContent reports whether every path branch changed since it forked from base is
// identical on base. Only those paths are compared, since base may have moved on. A
// branch that changed no paths is not proven merged: its commits are its only work.
func (c *Core) sameContent(ctx context.Context, repo, base, branch string) (bool, error) {
	fork, err := c.git(ctx, repo, "merge-base", base, branch)
	if err != nil {
		return false, err
	}
	// Not c.git: trimming would cut a path's leading space, and the trimmed path, absent
	// on both sides, would compare identical (and the branch be deleted unmerged).
	out, err := c.Run.Run(ctx, repo, "git", "-C", repo, "diff", "--name-only", "-z", "--no-renames", fork, branch)
	names := string(out)
	if err != nil || names == "" {
		return false, err
	}
	paths := strings.Split(strings.TrimRight(names, "\x00"), "\x00")
	_, err = c.git(ctx, repo, append([]string{"--literal-pathspecs", "diff", "--quiet", base, branch, "--"}, paths...)...)
	if isExit(err, 1) {
		return false, nil
	}
	return err == nil, err
}

// checkedOut returns the worktree of repo that has branch in use, as git counts it when it
// refuses to delete a branch: checked out, or being rebased or bisected there (which
// detaches HEAD, so worktree list says only "detached"). prunable means that worktree's
// directory is gone, and git worktree prune would free the branch.
func (c *Core) checkedOut(ctx context.Context, repo, branch string) (path string, prunable bool, err error) {
	out, err := c.git(ctx, repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", false, err
	}
	ref := "refs/heads/" + branch
	// Records are runs of NUL-ended fields, each record ended by an empty field.
	for rec := range strings.SplitSeq(strings.TrimSuffix(out, "\x00\x00"), "\x00\x00") {
		var wt string
		var mine, gone bool
		for f := range strings.SplitSeq(rec, "\x00") {
			switch k, v, _ := strings.Cut(f, " "); k {
			case "worktree":
				wt = v
			case "branch":
				mine = v == ref
			case "prunable":
				gone = true
			}
		}
		if !mine && !gone {
			if _, err := os.Stat(wt); os.IsNotExist(err) {
				continue // a locked worktree whose directory is gone: nothing runs a rebase there
			}
			gitDir, err := c.git(ctx, wt, "rev-parse", "--absolute-git-dir")
			if err != nil {
				return "", false, err
			}
			mine = busyWith(gitDir, branch)
		}
		if mine {
			return wt, gone, nil
		}
	}
	return "", false, nil
}

// busyWith reports whether a rebase or bisect in the worktree with gitDir started from
// branch.
func busyWith(gitDir, branch string) bool {
	for _, f := range []string{"rebase-merge/head-name", "rebase-apply/head-name", "BISECT_START"} {
		b, err := os.ReadFile(filepath.Join(gitDir, f))
		if err != nil {
			continue
		}
		if s := strings.TrimSpace(string(b)); s == "refs/heads/"+branch || s == branch {
			return true
		}
	}
	return false
}
