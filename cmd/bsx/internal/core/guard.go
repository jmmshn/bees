package core

import (
	"cmp"
	"context"
	"strings"

	"github.com/jmmshn/bees/cmd/bsx/internal/config"
	"github.com/jmmshn/bees/cmd/bsx/internal/guards"
)

// guardIn is what the guards of a worker in c.Repo are built from: the workspace kind,
// the repo's default branch names and, only when the config grants merges, GitHub's word
// on whether the default branch is protected. A lookup that fails is an error, never
// "unprotected" or "no default branch" (Rule G).
func (c *Core) guardIn(ctx context.Context) (guards.In, error) {
	in := guards.In{Kind: cmp.Or(c.Kind, config.GitWorktree), Git: true}
	origin, err := c.git(ctx, c.Repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil && !isExit(err, 1) {
		return in, err
	}
	_, origin, _ = strings.Cut(origin, "/")
	head, err := c.git(ctx, c.Repo, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil && !isExit(err, 1) {
		return in, err
	}
	in.DefaultBranches = guards.DefaultBranches(origin, head)
	if len(c.Guards.AllowMergeInto) == 0 {
		return in, nil
	}
	if c.Review == nil {
		return in, nil // no review host: nothing protects a default branch
	}
	repo, ok, err := c.Review.Repo(ctx, c.Repo)
	if err != nil || !ok {
		return in, err
	}
	in.Protected, err = c.Review.Protected(ctx, c.Repo, repo, cmp.Or(origin, head, "main"))
	return in, err
}

// CheckGuards refuses, at bs queen, a [guards] table every spawn would refuse
// (bees-12.20): an unprotected default branch under allow_merge_into, a glob bs can't use.
func (c *Core) CheckGuards(ctx context.Context) error {
	if len(c.Guards.Deny) == 0 && len(c.Guards.AllowMergeInto) == 0 {
		return nil
	}
	in, err := c.guardIn(ctx)
	if err != nil {
		return exit(ExitCantCheck, "", err, "bs queen: can't check [guards] against the repo")
	}
	if err := guards.Check(c.Guards, in); err != nil {
		return exit(ExitRefused, "bsx config", err, "bs queen: every spawn would refuse this [guards] table")
	}
	return nil
}

// deny is the guard rules of a worker spawned now.
func (c *Core) deny(ctx context.Context) ([]string, error) {
	in, err := c.guardIn(ctx)
	if err != nil {
		return nil, exit(ExitCantCheck, "", err, "bs spawn: can't build the worker's guards")
	}
	d, err := guards.Build(c.Guards, in)
	if err != nil {
		return nil, exit(ExitRefused, "bsx config", err, "bs spawn: its guards")
	}
	return d, nil
}
