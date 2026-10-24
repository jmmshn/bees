package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
)

// merged reports whether ws's branch is merged into its recorded base, and how bs knows or
// why not: the pilot's three proofs (origin/pilot/bees-12:internal/worker/merged.go),
// checked against the base and, when it has one, the base's upstream. A rebase or squash
// merge rewrites the commits, so ancestry is only the first proof; then every path the
// branch changed being identical on the base; then a merged change on the review host
// whose head is the branch tip. The local proofs run first, so the host is asked only
// when they fail.
// The proofs measure a commit, never a ref name (bees-13.18): the branch's tip, else, once
// the branch is renamed or deleted, its worktree's HEAD; with neither there is nothing to
// lose.
// err is a proof that couldn't run: never "not merged" (Rule G).
func (c *Core) merged(ctx context.Context, ws logbook.Workspace) (bool, string, error) {
	tip, why, err := c.tip(ctx, ws)
	switch {
	case err != nil:
		return false, "", err
	case tip == "":
		return true, why, nil
	}
	bases, err := c.bases(ctx, ws)
	if err != nil {
		return false, "", err
	}
	for _, b := range bases {
		switch in, err := c.ancestor(ctx, ws.Repo, tip, b); {
		case err != nil:
			return false, "", err
		case in:
			return true, "its commits are on " + short(b), nil
		}
		switch same, err := c.sameContent(ctx, ws.Repo, b, tip); {
		case err != nil:
			return false, "", fmt.Errorf("comparing with %s: %w", short(b), err)
		case same:
			return true, "the files it changed are identical on " + short(b) + " (a squash or rebase merge)", nil
		}
	}
	heads, skipped, err := c.landedHeads(ctx, ws)
	switch {
	case err != nil:
		return false, "", fmt.Errorf("not merged locally, and %s couldn't be asked: %w", c.Review.Name(), err)
	case slices.Contains(heads, tip):
		return true, "a merged change on " + c.Review.Name() + " has its tip as head", nil
	}
	names := make([]string, len(bases))
	for i, b := range bases {
		names[i] = short(b)
	}
	why = fmt.Sprintf("%s has work not on %s", why, strings.Join(names, " or "))
	if skipped != "" {
		why += " (" + skipped + ")"
	}
	return false, why, nil
}

// tip is the commit merged measures for ws, and what to call it: its branch's tip, else
// the HEAD of its directory while that is still a worktree of its repo (the branch renamed
// or deleted there), else "" with why nothing is lost.
func (c *Core) tip(ctx context.Context, ws logbook.Workspace) (tip, name string, err error) {
	switch tip, err := c.git(ctx, ws.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+ws.Branch); {
	case err == nil:
		return tip, ws.Branch, nil
	case !isExit(err, 1):
		return "", "", err
	}
	gone := fmt.Sprintf("branch %s no longer exists in %s", ws.Branch, ws.Repo)
	switch _, err := os.Stat(ws.Dir); {
	case ws.Dir == "" || os.IsNotExist(err):
		return "", gone + " and its worktree is gone: nothing to lose", nil
	case err != nil:
		return "", "", err
	}
	switch mine, err := c.isWorktree(ctx, ws); {
	case err != nil:
		return "", "", err
	case !mine:
		return "", gone + " and " + ws.Dir + " is no longer its worktree: nothing of its to lose", nil
	}
	switch tip, err := c.git(ctx, ws.Dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); {
	case err == nil:
		return tip, "the HEAD of " + ws.Dir, nil
	case !isExit(err, 1):
		return "", "", err
	}
	return "", gone + " and its worktree has no commit checked out: nothing to lose", nil
}

// landedHeads is the head commit of each merged change from ws's branch, of any age:
// merged's third proof, after a squash or rebase.
func (c *Core) landedHeads(ctx context.Context, ws logbook.Workspace) ([]string, string, error) {
	cs, skipped, err := c.Review.Changes(ctx, where(ws), time.Time{})
	var heads []string
	for _, ch := range cs {
		if ch.State == port.Merged {
			heads = append(heads, ch.HeadCommit)
		}
	}
	return heads, skipped, err
}

// base is the first of ws's bases: the ref its branch is measured against.
func (c *Core) base(ctx context.Context, ws logbook.Workspace) (string, error) {
	bs, err := c.bases(ctx, ws)
	if err != nil {
		return "", err
	}
	return bs[0], nil
}

// bases is where ws's branch lands, as full refs (a tag named like a branch never stands
// in for it, motif 3): its recorded base and, when it has one that still exists, the
// base's upstream (where a hosted merge is pulled; fetch --prune drops a merged stacked
// base's). A stacked base deleted since (its target merged and removed) falls back to the
// trunk recorded at spawn (ADR-0010 Amendment 4). A spawn from before goals recorded no
// base ref (main's v0) or the repo's path in its place (the pilot's v1), and one from
// before bees-13.63 no trunk: then its bases are the pilot's default branches, origin's
// default branch and the local one of the same name, or main.
func (c *Core) bases(ctx context.Context, ws logbook.Workspace) ([]string, error) {
	for _, b := range []string{ws.Base, ws.Trunk} {
		if b == "" || filepath.IsAbs(b) {
			continue
		}
		// A base deleted since lands where it landed, never "can't check" forever (motif 5).
		switch ok, err := c.isRef(ctx, ws.Repo, "refs/heads/"+b); {
		case err != nil:
			return nil, err
		case ok:
			out := []string{"refs/heads/" + b}
			up, err := c.git(ctx, ws.Repo, "for-each-ref", "--format=%(upstream)", "refs/heads/"+b)
			if err != nil || up == "" {
				return out, err
			}
			switch ok, err := c.isRef(ctx, ws.Repo, up); {
			case err != nil:
				return nil, err
			case ok:
				out = append(out, up)
			}
			return out, nil
		}
	}
	cands := []string{"refs/heads/main"}
	switch head, err := c.git(ctx, ws.Repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); {
	case err == nil && head != "":
		cands = []string{head, "refs/heads/" + strings.TrimPrefix(head, "refs/remotes/origin/")}
	case err != nil && !isExit(err, 1):
		return nil, err
	}
	var out []string
	for _, b := range cands {
		switch ok, err := c.isRef(ctx, ws.Repo, b); {
		case err != nil:
			return nil, err
		case ok:
			out = append(out, b)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no base recorded and no default branch (tried %s)", strings.Join(cands, ", "))
	}
	return out, nil
}

// isRef reports whether ref names a commit in repo.
func (c *Core) isRef(ctx context.Context, repo, ref string) (bool, error) {
	_, err := c.git(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if isExit(err, 1) {
		return false, nil
	}
	return err == nil, err
}

// short is ref as people say it: main for refs/heads/main, origin/main for
// refs/remotes/origin/main.
func short(ref string) string {
	if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return b
	}
	return strings.TrimPrefix(ref, "refs/remotes/")
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
