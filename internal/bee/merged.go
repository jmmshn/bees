package bee

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// GitHub is the slice of gh bs asks about merged pull requests.
type GitHub interface {
	// MergedHeads returns the head commit of each merged pull request whose head
	// branch is branch, in the GitHub repository of the git repo at repo.
	MergedHeads(ctx context.Context, repo, branch string) ([]string, error)
}

// dropBranch deletes branch once its work is proven to be on the default branch and
// otherwise returns why it kept it. A rebase or squash merge rewrites the commits, so
// git branch -d (ancestry) is only the first proof; then a merged PR whose head is the
// branch tip; then the branch's changed files being identical on the default branch.
// A probe that fails keeps the branch and says it could not check.
func dropBranch(ctx context.Context, gh GitHub, repo, branch string) (kept string) {
	if gitRun(ctx, repo, "branch", "-d", branch) == nil {
		return ""
	}
	tip, err := gitOut(ctx, repo, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return fmt.Sprintf("could not check whether it is merged: %v", err)
	}
	var ghNote string
	if gh != nil {
		heads, err := gh.MergedHeads(ctx, repo, branch)
		switch {
		case err != nil:
			ghNote = fmt.Sprintf(" (GitHub not checked: %v)", err)
		case slices.Contains(heads, strings.TrimSpace(tip)):
			return forceDelete(ctx, repo, branch)
		}
	}
	mains, err := defaultBranches(ctx, repo)
	if err != nil {
		return fmt.Sprintf("could not check whether it is merged: %v%s", err, ghNote)
	}
	for _, m := range mains {
		same, err := sameContent(ctx, repo, m, branch)
		if err != nil {
			return fmt.Sprintf("could not check whether it is merged into %s: %v%s", m, err, ghNote)
		}
		if same {
			return forceDelete(ctx, repo, branch)
		}
	}
	return fmt.Sprintf("it has work not on %s%s", strings.Join(mains, " or "), ghNote)
}

func forceDelete(ctx context.Context, repo, branch string) string {
	if err := gitRun(ctx, repo, "branch", "-D", branch); err != nil {
		return fmt.Sprintf("its work is merged, but deleting it failed: %v", err)
	}
	return ""
}

// defaultBranches is where merged work lands: origin's default branch (origin/HEAD)
// and the local branch of the same name, or main when there is no origin/HEAD. Only
// branches that exist are returned.
func defaultBranches(ctx context.Context, repo string) ([]string, error) {
	cands := []string{"main"}
	if head, err := gitOut(ctx, repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		head = strings.TrimSpace(head)
		cands = []string{head, strings.TrimPrefix(head, "origin/")}
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
