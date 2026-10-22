package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
)

// merged reports whether ws's branch is merged into its recorded base, and how bs knows or
// why not: the pilot's three proofs (origin/pilot/bees-12:internal/worker/merged.go),
// checked against the base and, when it has one, the base's upstream. A rebase or squash
// merge rewrites the commits, so ancestry is only the first proof; then every path the
// branch changed being identical on the base; then a merged GitHub pull request whose
// head is the branch tip. The local proofs run first, so gh is asked only when they fail.
// err is a proof that couldn't run: never "not merged" (Rule G).
func (c *Core) merged(ctx context.Context, ws logbook.Workspace) (bool, string, error) {
	tip, err := c.git(ctx, ws.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+ws.Branch)
	switch {
	case isExit(err, 1):
		return false, fmt.Sprintf("branch %s no longer exists in %s", ws.Branch, ws.Repo), nil
	case err != nil:
		return false, "", err
	}
	bases := []string{ws.Base}
	// An upstream the queen pulled a hosted merge into counts too; none is "".
	if up, err := c.git(ctx, ws.Repo, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+ws.Base); err != nil {
		return false, "", err
	} else if up != "" {
		bases = append(bases, up)
	}
	for _, b := range bases {
		switch in, err := c.ancestor(ctx, ws.Repo, ws.Branch, b); {
		case err != nil:
			return false, "", err
		case in:
			return true, "its commits are on " + b, nil
		}
		switch same, err := c.sameContent(ctx, ws.Repo, b, ws.Branch); {
		case err != nil:
			return false, "", fmt.Errorf("comparing with %s: %w", b, err)
		case same:
			return true, "the files it changed are identical on " + b + " (a squash or rebase merge)", nil
		}
	}
	switch heads, err := c.mergedHeads(ctx, ws.Repo, ws.Branch); {
	case err != nil:
		return false, "", fmt.Errorf("not merged locally, and GitHub couldn't be asked: %w", err)
	case slices.Contains(heads, tip):
		return true, "a merged pull request has its tip as head", nil
	}
	return false, fmt.Sprintf("%s has work not on %s", ws.Branch, strings.Join(bases, " or ")), nil
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
	names, err := c.git(ctx, repo, "diff", "--name-only", "-z", "--no-renames", fork, branch)
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

// mergedHeads is the head commit of each merged pull request whose head branch is branch,
// asked of gh only when repo's origin is on GitHub: with no GitHub remote no pull request
// could have merged it, and gh isn't asked.
func (c *Core) mergedHeads(ctx context.Context, repo, branch string) ([]string, error) {
	url, err := c.git(ctx, repo, "config", "--get", "remote.origin.url")
	switch {
	case isExit(err, 1):
		return nil, nil
	case err != nil:
		return nil, err
	case !strings.Contains(url, "github.com"):
		return nil, nil
	}
	out, err := c.Run.Run(ctx, repo, "gh", "pr", "list", "--head", branch, "--state", "merged", "--json", "headRefOid")
	if err != nil {
		return nil, err
	}
	var prs []struct {
		OID string `json:"headRefOid"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	var heads []string
	for _, p := range prs {
		heads = append(heads, p.OID)
	}
	return heads, nil
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
