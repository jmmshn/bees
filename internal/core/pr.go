package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
)

// look is one run of checks over one spawn, each change and the workspace's commits
// looked up once.
type look struct {
	c     *Core
	sp    logbook.Spawn
	h     Handed // h.PRs: the changes a done recorded before Amendment 4 named
	views map[string]port.Change
	errs  map[string]error
	lags  map[string]string // change URL → lag's answer
	found *found            // the worker's change, once looked for
}

// found is the change a pr goal is judged by, or why there is none (and what to run).
type found struct {
	c         port.Change
	why, next string
	err       error
}

// where is ws as a review host sees it: one spelling (motif 3).
func where(ws logbook.Workspace) port.Where {
	w := port.Where{Repo: cmp.Or(ws.Repo, ws.Dir), Dir: ws.Dir, Branch: ws.Branch}
	if ws.Trunk != "" && ws.Base != ws.Trunk {
		w.Base = ws.Base // a stacked branch's change lands on its target's head
	}
	return w
}

// theChange is the change sp's pr goal is judged by, found from its branch: the worker
// names none (ADR-0010 Amendment 4). Candidates are the changes created since the spawn,
// plus any still open whatever its age (a re-spawn on a kept branch fixing its open
// change); an old merged one never counts (motif 2). One open is it; two or more is a
// failure naming each (GitHub's unit is one change); none open is the newest; none at all
// says how to make one.
func (c *Core) theChange(ctx context.Context, sp logbook.Spawn) found {
	w := where(sp.Workspace)
	cs, skipped, err := c.Review.Changes(ctx, w, sp.TS.Truncate(time.Second))
	if err != nil {
		return found{err: err}
	}
	isOpen := func(ch port.Change) bool { return ch.State == port.Open }
	if !slices.ContainsFunc(cs, isOpen) {
		old, _, err := c.Review.Changes(ctx, w, time.Time{})
		if err != nil {
			return found{err: err}
		}
		cs = append(cs, slices.DeleteFunc(old, func(ch port.Change) bool { return !isOpen(ch) })...)
	}
	var open []string
	for _, ch := range cs {
		if isOpen(ch) {
			open = append(open, ch.URL)
		}
	}
	switch {
	case len(open) > 1:
		return found{why: fmt.Sprintf("%d open changes from %s: %s; hand off one", len(open), w.Branch, strings.Join(open, ", ")),
			next: "close all but one, then " + bsname.Cmd + " done"}
	case len(open) == 1:
		return found{c: cs[slices.IndexFunc(cs, isOpen)]}
	case len(cs) > 0:
		return found{c: cs[0]}
	}
	why := "no change from " + w.Branch + " since this spawn"
	if skipped != "" {
		why += " (" + skipped + ")"
	}
	return found{why: why, next: c.Review.Create(w)}
}

// change is the worker's change, looked for once.
func (k *look) change(ctx context.Context) found {
	if k.found == nil {
		f := k.c.theChange(ctx, k.sp)
		k.found = &f
	}
	return *k.found
}

// outcome is a check's answer: failed, passed, or pending (not failed, not passed yet).
type outcome uint8

const (
	failed outcome = iota
	passed
	pending
)

func (c *Core) look(sp logbook.Spawn, h Handed) *look {
	return &look{c: c, sp: sp, h: h, views: map[string]port.Change{}, errs: map[string]error{}, lags: map[string]string{}}
}

// at is where the review host is asked: the project's checkout, which outlives a
// workspace prune has just removed.
func (k *look) at() string { return cmp.Or(k.sp.Workspace.Repo, k.sp.Workspace.Dir) }

func (k *look) view(ctx context.Context, r port.Ref) (port.Change, error) {
	if v, ok := k.views[r.URL]; ok {
		return v, nil
	}
	if err, ok := k.errs[r.URL]; ok {
		return port.Change{}, err
	}
	v, err := k.c.Review.View(ctx, k.at(), r)
	if err != nil {
		k.errs[r.URL] = err
		return port.Change{}, err
	}
	k.views[r.URL] = v
	return v, nil
}

// changes is the changes goal g's checks read: a pr goal's change found from the branch
// (or the ones a done recorded before Amendment 4 named), a push or review goal's target.
// why says why there is none to read.
func (k *look) changes(ctx context.Context, g goal.Goal) (vs []port.Change, why string, err error) {
	urls := k.h.PRs
	if g.Type == "pr" && len(urls) == 0 {
		f := k.change(ctx)
		return []port.Change{f.c}, f.why, f.err
	}
	if g.Type == "push" || g.Type == "review" {
		urls = []string{g.Target}
	}
	var whys []string
	for _, u := range urls {
		r, err := k.c.Review.Parse(u)
		if err != nil {
			return nil, "", err
		}
		v, err := k.view(ctx, r)
		if errors.Is(err, port.ErrNoChange) {
			whys = append(whys, r.URL+": no such change in "+r.Repo)
			continue
		} else if err != nil {
			return nil, "", fmt.Errorf("%s: %w", r.URL, err)
		}
		vs = append(vs, v)
	}
	return vs, strings.Join(whys, "; "), nil
}

// isPRCheck reports whether name reads changes on the review host.
func isPRCheck(name string) bool {
	switch name {
	case goal.PRExists, goal.PROpen, goal.PRNotDraft, goal.PRHead, goal.PRWorkspaceCommit, goal.PRPushed, goal.PRCurrent,
		goal.CIGreen, goal.PRMerged, goal.PRLanded, goal.PRClosed, goal.PushMoved:
		return true
	}
	return false
}

// prCheck runs change check name over each of g's changes: it passes when every one does,
// and is pending when the rest are; a change that couldn't be looked at is an error, never
// a fail (Rule G).
func (k *look) prCheck(ctx context.Context, g goal.Goal, name string) (outcome, string, error) {
	vs, why, err := k.changes(ctx, g)
	if err != nil || why != "" {
		return failed, why, err
	}
	var fails, oks []string
	o := passed
	for _, v := range vs {
		ro, why, err := k.one(ctx, name, v)
		switch {
		case err != nil:
			return failed, "", fmt.Errorf("%s: %w", v.URL, err)
		case ro != passed:
			if o != failed {
				o = ro
			}
			fails = append(fails, v.URL+": "+why)
		case why != "":
			oks = append(oks, v.URL+": "+why)
		}
	}
	if o != passed {
		return o, strings.Join(fails, "; "), nil
	}
	return passed, strings.Join(oks, "; "), nil
}

func (k *look) one(ctx context.Context, name string, v port.Change) (outcome, string, error) {
	c := k.c
	switch name {
	case goal.PRExists:
		return passed, "", nil
	case goal.PROpen:
		return unless(v.State == port.Open, v.State)
	case goal.PRNotDraft:
		return unless(!v.Draft, "a draft: mark it ready")
	case goal.PRMerged:
		return unless(v.State == port.Merged, v.State)
	case goal.PRLanded:
		trunk := k.sp.Workspace.Trunk
		switch {
		case v.State != port.Merged:
			return failed, v.State, nil
		case trunk != "" && v.Base != trunk:
			return failed, fmt.Sprintf("merged into %s, not %s: it never lands on %s itself (once %s merges there, %s goal %s --waive %s)",
				v.Base, trunk, trunk, v.Base, bsname.Cmd, k.sp.Name, goal.PRLanded), nil
		}
		return passed, "", nil
	case goal.PRClosed:
		return unless(v.State == port.Merged || v.State == port.Closed, v.State)
	case goal.PRHead, goal.PRWorkspaceCommit, goal.PRPushed, goal.PushMoved:
		return k.prHead(ctx, v)
	case goal.PRCurrent, goal.CIGreen:
		// What the host says of a head the workspace has pushed past is about the old one.
		if up, err := k.lag(ctx, v); err != nil {
			return failed, "", err
		} else if up != "" {
			return pending, fmt.Sprintf("pending: %s still shows %s, not %s pushed since", c.Review.Name(), oid(v.HeadCommit), oid(up)), nil
		}
		if name == goal.CIGreen {
			return k.ciGreen(ctx, v)
		}
		n, err := c.Review.Behind(ctx, k.at(), v)
		if err != nil {
			return failed, "", err
		}
		return unless(n == 0, fmt.Sprintf("%d commits behind %s: bring %s in, then push", n, v.Base, v.Base))
	}
	return failed, "", fmt.Errorf("no check %s", name)
}

// prHead is the change's head being the worker's branch tip, and the tip ahead of where
// the branch started at spawn (Workspace.Start): bs found the change from the branch, so
// that is all a handoff needs of it (ADR-0010 Amendment 4). A head the host lags on is
// pending; one holding commits the branch lacks says pull (unpushed). A spawn recorded
// before bs kept a start is said to be unchecked there, never silently passed (Rule G).
func (k *look) prHead(ctx context.Context, v port.Change) (outcome, string, error) {
	c, ws := k.c, k.sp.Workspace
	tip, ok, err := c.VCS.Branch(ctx, ws.Dir, ws.Branch)
	switch {
	case err != nil:
		return failed, "", err
	case !ok:
		return failed, fmt.Sprintf("branch %s is gone from %s", ws.Branch, ws.Dir), nil
	}
	note := ""
	switch {
	case ws.Start == "":
		note = "spawned before bs recorded where " + ws.Branch + " started: not checked that it moved since"
	case tip == ws.Start:
		return failed, fmt.Sprintf("%s is where it started at spawn (%s): commit your work, then push it", ws.Branch, oid(tip)), nil
	default:
		if ahead, err := c.VCS.Descends(ctx, ws.Dir, ws.Start, tip); err != nil {
			return failed, "", err
		} else if !ahead {
			return failed, fmt.Sprintf("%s at %s doesn't descend from %s, where it started at spawn", ws.Branch, oid(tip), oid(ws.Start)), nil
		}
	}
	if v.HeadCommit != tip {
		return k.unpushed(ctx, v, tip)
	}
	return passed, note, nil
}

// lag is the commit the workspace pushed v's head to while the host still shows an older
// head (GitHub lags a push by seconds, seen live): the branch's tip that descends from the
// host's head. "" when the host shows what was pushed, or the workspace can't say.
func (k *look) lag(ctx context.Context, v port.Change) (string, error) {
	if up, ok := k.lags[v.URL]; ok {
		return up, nil
	}
	c, dir := k.c, k.sp.Workspace.Dir
	up, ok, err := c.VCS.Pushed(ctx, dir, v.Head)
	if err != nil || !ok || up == v.HeadCommit || v.State != port.Open {
		return "", err
	}
	if has, err := c.VCS.Has(ctx, dir, v.HeadCommit); err != nil || !has {
		return "", err
	}
	b := k.sp.Workspace.Branch
	if b == "" { // a push workspace made on no branch, before Amendment 4: no tip to compare
		return "", nil
	}
	if tip, ok, err := c.VCS.Branch(ctx, dir, b); err != nil || !ok || tip != up {
		return "", err
	}
	ahead, err := c.VCS.Descends(ctx, dir, v.HeadCommit, up)
	if err != nil || !ahead {
		return "", err
	}
	k.lags[v.URL] = up
	return up, nil
}

// unpushed says why a change's head isn't the local branch's tip. The host's head holding
// commits the branch lacks (someone pushed onto it, or "Update branch") is a fail that says
// to pull them, never pending: waiting would never end (motif 5). The branch ahead of the
// host's head is pending when it was pushed already (lag), else "push it": onto a push
// goal's target, by the command that names its head.
func (k *look) unpushed(ctx context.Context, v port.Change, tip string) (outcome, string, error) {
	c, dir, host := k.c, k.sp.Workspace.Dir, k.c.Review.Name()
	has, err := c.VCS.Has(ctx, dir, v.HeadCommit)
	if err != nil {
		return failed, "", err
	}
	ahead := false
	if has {
		if ahead, err = c.VCS.Descends(ctx, dir, v.HeadCommit, tip); err != nil {
			return failed, "", err
		}
	}
	if !ahead {
		return failed, fmt.Sprintf("its head %s on %s has commits %s lacks (at %s): pull them, then push", oid(v.HeadCommit), host, v.Head, oid(tip)), nil
	}
	if up, err := k.lag(ctx, v); err != nil {
		return failed, "", err
	} else if up == tip {
		return pending, fmt.Sprintf("pending: %s was pushed at %s, and %s still shows %s", v.Head, oid(tip), host, oid(v.HeadCommit)), nil
	}
	if b := k.sp.Workspace.Branch; v.Head != b {
		return failed, fmt.Sprintf("%s is at %s here, and %s at %s on %s: %s", b, oid(tip), v.Head, oid(v.HeadCommit), host, c.Review.PushHint(ctx, dir, v)), nil
	}
	return failed, fmt.Sprintf("%s is at %s here and at %s on %s: push it", v.Head, oid(tip), oid(v.HeadCommit), host), nil
}

// targetOpen is a stacked change's target still open, so there is something to land on;
// or the worker's change retargeted off it, onto whatever the queen chose. A target merged
// (or closed) meanwhile names the retarget, never pending: GitHub doesn't always move a
// stacked change itself (ADR-0010 Amendment 4).
func (k *look) targetOpen(ctx context.Context, g goal.Goal) (outcome, string, error) {
	f := k.change(ctx)
	if f.err != nil || f.why != "" {
		return failed, f.why, f.err
	}
	r, err := k.c.Review.Parse(g.Target)
	if err != nil {
		return failed, "", err
	}
	t, err := k.view(ctx, r)
	switch {
	case errors.Is(err, port.ErrNoChange):
		return failed, r.URL + ": no such change in " + r.Repo, nil
	case err != nil:
		return failed, "", fmt.Errorf("%s: %w", r.URL, err)
	case t.State == port.Open:
		return passed, "", nil
	case f.c.Base != t.Head:
		return passed, fmt.Sprintf("%s is %s, and %s was retargeted onto %s", t.URL, t.State, f.c.URL, f.c.Base), nil
	}
	trunk := cmp.Or(k.sp.Workspace.Trunk, t.Base)
	return failed, fmt.Sprintf("its target %s is %s, and %s still lands on %s: %s", t.URL, t.State, f.c.URL, t.Head, k.c.Review.Retarget(f.c, trunk)), nil
}

// ciGreen is every required CI check passing (skipped ones count; keeper, 2026-10-22).
// A check the base's protection requires that CI hasn't reported yet is pending, never
// read as "none required" (Rule G: right after a push the host lists none); a pass is
// vacuous only when the protection requires none, and says so.
func (k *look) ciGreen(ctx context.Context, v port.Change) (outcome, string, error) {
	cs, err := k.c.Review.CI(ctx, k.at(), v)
	if err != nil {
		return failed, "", err
	}
	req, err := k.c.Review.RequiredChecks(ctx, k.at(), v)
	if err != nil {
		return failed, "", fmt.Errorf("reading %s's required checks: %w", v.Base, err)
	}
	if len(cs) == 0 && len(req) == 0 {
		return passed, "vacuous pass: " + v.Base + " requires no checks", nil
	}
	var fail, wait []string
	for _, name := range req {
		if !slices.ContainsFunc(cs, func(c port.CICheck) bool { return c.Name == name }) {
			wait = append(wait, name+" (not reported yet)")
		}
	}
	for _, c := range cs {
		switch c.Bucket {
		case port.CIPass, port.CISkipping:
		case port.CIPending:
			wait = append(wait, c.Name)
		default:
			fail = append(fail, c.Name)
		}
	}
	var why []string
	if len(fail) > 0 {
		why = append(why, "failing: "+strings.Join(fail, ", "))
	}
	if len(wait) > 0 {
		why = append(why, "pending: "+strings.Join(wait, ", "))
	}
	switch {
	case len(fail) > 0:
		return failed, strings.Join(why, "; "), nil
	case len(wait) > 0:
		return pending, strings.Join(why, "; "), nil
	}
	return passed, "", nil
}

// of is ok as an outcome.
func of(ok bool) outcome {
	if ok {
		return passed
	}
	return failed
}

func unless(ok bool, why string) (outcome, string, error) {
	if ok {
		return passed, "", nil
	}
	return failed, why, nil
}

func oid(id string) string { return id[:min(len(id), 9)] }
