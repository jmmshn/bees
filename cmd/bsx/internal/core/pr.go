package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/goal"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// workspaceCommits is every commit made in sp's workspace since the first spawn that
// recorded its directory, as its VCS reads them: the only commits a handed-off change may
// be judged by, so a typo can't name another worker's PR (ADR-0010).
func (c *Core) workspaceCommits(ctx context.Context, sp logbook.Spawn) ([]string, error) {
	since, ok, err := c.Logbook.FirstSpawnAt(ctx, sp.Workspace.Dir)
	if err != nil {
		return nil, err
	}
	if !ok {
		since = sp.TS
	}
	return c.VCS.Commits(ctx, sp.Workspace.Dir, since.Truncate(time.Second))
}

// look is one run of checks over one spawn, each change and the workspace's commits
// looked up once.
type look struct {
	c     *Core
	sp    logbook.Spawn
	h     Handed // h.PRs: every change handed off so far
	views map[string]port.Change
	errs  map[string]error
	wc    []string
	wcErr error
	wcOK  bool
	// pending is set by a ci_green that failed only on checks still running.
	pending bool
}

func (c *Core) look(sp logbook.Spawn, h Handed) *look {
	return &look{c: c, sp: sp, h: h, views: map[string]port.Change{}, errs: map[string]error{}}
}

func (k *look) view(ctx context.Context, r port.Ref) (port.Change, error) {
	if v, ok := k.views[r.URL]; ok {
		return v, nil
	}
	if err, ok := k.errs[r.URL]; ok {
		return port.Change{}, err
	}
	v, err := k.c.Review.View(ctx, k.sp.Workspace.Dir, r)
	if err != nil {
		k.errs[r.URL] = err
		return port.Change{}, err
	}
	k.views[r.URL] = v
	return v, nil
}

func (k *look) commits(ctx context.Context) ([]string, error) {
	if !k.wcOK {
		k.wc, k.wcErr = k.c.workspaceCommits(ctx, k.sp)
		k.wcOK = true
	}
	return k.wc, k.wcErr
}

// refs is the changes part p's checks read: a pr part's handed-off ones, a push or
// review part's target.
func (k *look) refs(p goal.Part) ([]port.Ref, error) {
	urls := k.h.PRs
	if p.Type != "pr" {
		urls = []string{p.Target}
	}
	var out []port.Ref
	for _, u := range urls {
		r, err := k.c.Review.Parse(u)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// isPRCheck reports whether name reads changes on the review host.
func isPRCheck(name string) bool {
	switch name {
	case goal.PRExists, goal.PROpen, goal.PRNotDraft, goal.PRWorkspaceCommit, goal.PRPushed, goal.PRCurrent,
		goal.CIGreen, goal.PRMerged, goal.PRClosed, goal.PushMoved, goal.ReviewComment:
		return true
	}
	return false
}

// prCheck runs change check name over each of p's changes: it passes when every one does;
// a change that couldn't be looked at is an error, never a fail (Rule G).
func (k *look) prCheck(ctx context.Context, p goal.Part, name string) (bool, string, error) {
	refs, err := k.refs(p)
	if err != nil {
		return false, "", err
	}
	if len(refs) == 0 {
		return false, "no PR handed off: bs done --pr <url>", nil
	}
	var fails, oks []string
	allPending := true
	for _, r := range refs {
		k.pending = false
		ok, why, err := k.one(ctx, name, r)
		switch {
		case err != nil:
			return false, "", fmt.Errorf("%s: %w", r.URL, err)
		case !ok:
			allPending = allPending && k.pending
			fails = append(fails, r.URL+": "+why)
		case why != "":
			oks = append(oks, r.URL+": "+why)
		}
	}
	k.pending = len(fails) > 0 && allPending
	if len(fails) > 0 {
		return false, strings.Join(fails, "; "), nil
	}
	return true, strings.Join(oks, "; "), nil
}

func (k *look) one(ctx context.Context, name string, r port.Ref) (bool, string, error) {
	v, err := k.view(ctx, r)
	if errors.Is(err, port.ErrNoChange) {
		return false, "no such change in " + r.Repo, nil
	} else if err != nil {
		return false, "", err
	}
	c, dir := k.c, k.sp.Workspace.Dir
	switch name {
	case goal.PRExists:
		return true, "", nil
	case goal.PROpen:
		return unless(v.State == "open", v.State)
	case goal.PRNotDraft:
		return unless(!v.Draft, "a draft: mark it ready")
	case goal.PRMerged:
		return unless(v.State == "merged", v.State)
	case goal.PRClosed:
		return unless(v.State == "merged" || v.State == "closed", v.State)
	case goal.PRWorkspaceCommit:
		wc, err := k.commits(ctx)
		if err != nil {
			return false, "", err
		}
		if slices.ContainsFunc(v.Commits, func(cm string) bool { return slices.Contains(wc, cm) }) {
			return true, "", nil
		}
		return false, fmt.Sprintf("none of its %d commits was made in %s: is it another worker's?", len(v.Commits), dir), nil
	case goal.PRPushed:
		tip, ok, err := c.VCS.Branch(ctx, dir, v.Head)
		switch {
		case err != nil:
			return false, "", err
		case ok && tip != v.HeadCommit:
			return false, fmt.Sprintf("%s is at %s here and at %s on %s: push it", v.Head, short(tip), short(v.HeadCommit), c.Review.Name()), nil
		case ok:
			return true, "", nil
		}
		// No local branch of that name (pushed from a detached HEAD, or a host with no
		// branches): its head must be a commit made here.
		wc, err := k.commits(ctx)
		if err != nil {
			return false, "", err
		}
		if !slices.Contains(wc, v.HeadCommit) {
			return false, fmt.Sprintf("its head %s is no commit made in %s", short(v.HeadCommit), dir), nil
		}
		return true, "", nil
	case goal.PRCurrent:
		n, err := c.Review.Behind(ctx, dir, v)
		if err != nil {
			return false, "", err
		}
		return unless(n == 0, fmt.Sprintf("%d commits behind %s: bring %s in, then push", n, v.Base, v.Base))
	case goal.CIGreen:
		return k.ciGreen(ctx, v)
	case goal.PushMoved:
		return k.pushMoved(ctx, v)
	case goal.ReviewComment:
		mark, since := "bs:"+string(k.sp.ID), k.sp.TS.Truncate(time.Second)
		if slices.ContainsFunc(v.Notes, func(n port.Note) bool { return !n.At.Before(since) && strings.Contains(n.Body, mark) }) {
			return true, "", nil
		}
		return false, "no comment or review since the spawn carries " + mark, nil
	}
	return false, "", fmt.Errorf("no check %s", name)
}

// ciGreen is every CI check passing (skipped ones count). None registered is pending
// while the base's tip has checks, since CI hasn't picked the change up yet; with none
// there either it passes vacuously, and says so.
func (k *look) ciGreen(ctx context.Context, v port.Change) (bool, string, error) {
	dir := k.sp.Workspace.Dir
	cs, err := k.c.Review.CI(ctx, dir, v)
	if err != nil {
		return false, "", err
	}
	if len(cs) == 0 {
		n, err := k.c.Review.BaseCI(ctx, dir, v)
		if err != nil {
			return false, "", err
		}
		if n > 0 {
			k.pending = true
			return false, fmt.Sprintf("pending: no checks registered yet, while %s's tip has %d", v.Base, n), nil
		}
		return true, "vacuous pass: neither it nor " + v.Base + " has CI checks", nil
	}
	var fail, pending []string
	for _, c := range cs {
		switch c.Bucket {
		case "pass", "skipping":
		case "pending":
			pending = append(pending, c.Name)
		default:
			fail = append(fail, c.Name)
		}
	}
	k.pending = len(fail) == 0 && len(pending) > 0
	var why []string
	if len(fail) > 0 {
		why = append(why, "failing: "+strings.Join(fail, ", "))
	}
	if len(pending) > 0 {
		why = append(why, "pending: "+strings.Join(pending, ", "))
	}
	return unless(len(why) == 0, strings.Join(why, "; "))
}

// pushMoved is the target's head descending from a commit made in the workspace; a head
// the workspace lacks is fetched first.
func (k *look) pushMoved(ctx context.Context, v port.Change) (bool, string, error) {
	c, dir := k.c, k.sp.Workspace.Dir
	wc, err := k.commits(ctx)
	if err != nil {
		return false, "", err
	}
	if slices.Contains(wc, v.HeadCommit) {
		return true, "", nil
	}
	if len(wc) == 0 {
		return false, "nothing was committed in " + dir, nil
	}
	if has, err := c.VCS.Has(ctx, dir, v.HeadCommit); err != nil {
		return false, "", err
	} else if !has {
		if err := c.Review.Fetch(ctx, dir, v); err != nil {
			return false, "", err
		}
	}
	for _, w := range wc {
		if in, err := c.VCS.Descends(ctx, dir, w, v.HeadCommit); err != nil {
			return false, "", err
		} else if in {
			return true, "", nil
		}
	}
	return false, fmt.Sprintf("its head %s descends from no commit made in %s: %s", short(v.HeadCommit), dir, c.Review.PushHint(ctx, dir, v)), nil
}

// unless is a check's outcome: why it failed, only when it did. A passing check says
// nothing, so its detail never reads as a failure (motif 11).
func unless(ok bool, why string) (bool, string, error) {
	if ok {
		return true, "", nil
	}
	return false, why, nil
}

func short(oid string) string { return oid[:min(len(oid), 9)] }
