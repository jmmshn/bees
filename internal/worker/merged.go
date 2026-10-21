package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// GitHub is the slice of gh bs asks about merged pull requests.
type GitHub interface {
	// MergedHeads returns the head commit of each merged pull request whose head
	// branch is branch, in the GitHub repository of the git repo at repo.
	MergedHeads(ctx context.Context, repo, branch string) ([]string, error)
	// Protected reports whether GitHub protects the default branch of the git repo at repo.
	Protected(ctx context.Context, repo string) (bool, error)
}

// Merge is whether a branch's work is on the default branch.
type Merge int

const (
	NotMerged Merge = iota
	Merged
	Unknown // a probe failed, so whether the work landed is unknown
)

// merged decides whether branch's work is on the default branch, and says how it
// knows or why not. A rebase or squash merge rewrites the commits, so ancestry is only
// the first proof; then a merged PR whose head is the branch tip; then every path the
// branch changed being identical on the default branch.
func merged(ctx context.Context, gh GitHub, repo, branch string) (Merge, string) {
	tip, err := gitOut(ctx, repo, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return Unknown, err.Error()
	}
	var ghNote string
	if gh != nil {
		heads, err := gh.MergedHeads(ctx, repo, branch)
		switch {
		case err != nil:
			ghNote = fmt.Sprintf(" (GitHub not checked: %v)", err)
		case slices.Contains(heads, strings.TrimSpace(tip)):
			return Merged, "a merged pull request has its tip as head"
		}
	}
	mains, err := defaultBranches(ctx, repo)
	if err != nil {
		return Unknown, err.Error() + ghNote
	}
	for _, m := range mains {
		if in, err := ancestor(ctx, repo, branch, m); err != nil {
			return Unknown, err.Error() + ghNote
		} else if in {
			return Merged, "its commits are on " + m
		}
		same, err := sameContent(ctx, repo, m, branch)
		if err != nil {
			return Unknown, fmt.Sprintf("comparing with %s: %v%s", m, err, ghNote)
		}
		if same {
			return Merged, "the files it changed are identical on " + m
		}
	}
	return NotMerged, fmt.Sprintf("it has work not on %s%s", strings.Join(mains, " or "), ghNote)
}

// checkedOut returns the worktree of repo that has branch in use, as git counts it when
// it refuses to delete a branch: checked out, or being rebased or bisected there (which
// detaches HEAD, so worktree list says only "detached"). prunable means that worktree's
// directory is gone, and git worktree prune would free the branch.
func checkedOut(ctx context.Context, repo, branch string) (path string, prunable bool, err error) {
	out, err := gitOut(ctx, repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", false, err
	}
	ref := "refs/heads/" + branch
	// Records are runs of NUL-ended fields, each record ended by an empty field.
	for rec := range strings.SplitSeq(strings.TrimSuffix(out, "\x00\x00"), "\x00\x00") {
		var wt, gitDir string
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
			if gitDir, err = gitOut(ctx, wt, "rev-parse", "--absolute-git-dir"); err != nil {
				return "", false, err
			}
			mine = busyWith(strings.TrimSpace(gitDir), branch)
		}
		if mine {
			return wt, gone, nil
		}
	}
	return "", false, nil
}

// busyWith reports whether a rebase or bisect in the worktree with gitDir started from branch.
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

// ancestor reports whether commit a is an ancestor of b.
func ancestor(ctx context.Context, repo, a, b string) (bool, error) {
	err := gitRun(ctx, repo, "merge-base", "--is-ancestor", a, b)
	var ee *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return false, nil
	}
	return false, err
}

// defaultBranches is where merged work lands: origin's default branch (origin/HEAD)
// and the local branch of the same name, or main when there is no origin/HEAD. Only
// branches that exist are returned.
func defaultBranches(ctx context.Context, repo string) ([]string, error) {
	cands := []string{"main"}
	if head, err := originHead(ctx, repo); err == nil && head != "" {
		cands = []string{"origin/" + head, head}
	}
	var out []string
	for _, c := range cands {
		if gitRun(ctx, repo, "rev-parse", "--verify", "--quiet", c+"^{commit}") == nil {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no default branch (tried %s)", strings.Join(cands, ", "))
	}
	return out, nil
}

// originHead is the branch origin/HEAD points at (develop for origin/develop), or ""
// when it points nowhere: no origin, or a clone that never set it. Any other git failure
// is an error, so a guard built from it fails closed.
func originHead(ctx context.Context, repo string) (string, error) {
	out, err := gitOut(ctx, repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return "", nil
	case err != nil:
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(out), "origin/"), nil
}

// sameContent reports whether every path branch changed since it forked from main is
// identical on main. Only those paths are compared, since main may have moved on. A
// branch that changed no paths is not proven merged: its commits are its only work.
func sameContent(ctx context.Context, repo, main, branch string) (bool, error) {
	base, err := gitOut(ctx, repo, "merge-base", main, branch)
	if err != nil {
		return false, err
	}
	names, err := gitOut(ctx, repo, "diff", "--name-only", "-z", "--no-renames", strings.TrimSpace(base), branch)
	if err != nil {
		return false, err
	}
	paths := strings.Split(strings.TrimRight(names, "\x00"), "\x00")
	if names == "" {
		return false, nil
	}
	err = gitRun(ctx, repo, append([]string{"--literal-pathspecs", "diff", "--quiet", main, branch, "--"}, paths...)...)
	var ee *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return false, nil
	}
	return false, err
}

func gitRun(ctx context.Context, repo string, args ...string) error {
	_, err := gitOut(ctx, repo, args...)
	return err
}

// gitOut runs git in repo. A failure carries git's stderr; an *exec.ExitError stays
// reachable with errors.As.
func gitOut(ctx context.Context, repo string, args ...string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, msg)
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return string(out), nil
}
