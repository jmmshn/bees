package scenario_test

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/fake"
	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
)

// toyRepo is the GitHub repo the toy's origin names.
const toyRepo = "https://github.com/x/toy"

// github makes the toy's origin github.com/x/toy, standing for a bare repo the fake gh
// reads as GitHub's copy (url.insteadOf: git config still says github.com).
func (x *toy) github() string {
	x.t.Helper()
	bare := filepath.Join(filepath.Dir(x.repo), "origin.git")
	x.git(filepath.Dir(x.repo), "init", "-q", "--bare", "-b", "main", bare)
	x.git(x.repo, "remote", "add", "origin", toyRepo+".git")
	x.git(x.repo, "config", "url."+bare+".insteadOf", toyRepo+".git")
	x.git(x.repo, "push", "-q", "-u", "origin", "main")
	x.git(x.repo, "remote", "set-head", "origin", "main")
	x.gh.Git = bare
	return bare
}

// openPR opens PR n on the fake GitHub from branch head onto main.
func (x *toy) openPR(n int, head string, mods ...func(*fake.PR)) string {
	url := fmt.Sprintf("%s/pull/%d", toyRepo, n)
	pr := &fake.PR{URL: url, Author: "jmmshn", Head: head, Base: "main", State: "OPEN", Created: time.Now()}
	for _, m := range mods {
		m(pr)
	}
	x.gh.PRs[url] = pr
	return url
}

// mergePR squash-merges url into main on the fake GitHub and marks it merged.
func (x *toy) mergePR(url string) {
	x.t.Helper()
	pr := x.gh.PRs[url]
	x.git(x.repo, "fetch", "-q", "origin")
	pr.HeadOID = x.git(x.repo, "rev-parse", "origin/"+pr.Head)
	x.git(x.repo, "merge", "-q", "--squash", "origin/"+pr.Head)
	if x.git(x.repo, "diff", "--cached", "--name-only") != "" { // nothing new when a merged PR held it
		x.git(x.repo, "commit", "-qm", "squash "+pr.Head)
	}
	x.git(x.repo, "push", "-q", "origin", "main")
	pr.State = "MERGED"
}

// push pushes dir's HEAD to branch on origin, as the worker would.
func (x *toy) push(dir, branch string) {
	x.t.Helper()
	x.git(dir, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
}

func ci(states ...string) func(*fake.PR) {
	return func(pr *fake.PR) {
		pr.CI = nil
		for i, s := range states {
			pr.CI = append(pr.CI, fake.Check{Name: fmt.Sprintf("job%d", i+1), State: s})
		}
	}
}

// backend is one review host and VCS the PR-goal scenarios run on: GitHub through the
// fake gh over real git (the default config), and fake.CLHost, which has neither git nor
// gh. Each scenario runs on both: the goal checks know only the ports.
type backend interface {
	setup(x *toy, remote bool)
	commit(x *toy, dir string)
	open(x *toy, n int, dir, head string, draft bool) string // publish dir's head as change n
	update(x *toy, n int, dir string)                        // push dir's head onto change n
	ci(x *toy, n int, buckets ...string)                     // pass, fail or pending; a ? suffix: not required
	ciLater(x *toy, n int, buckets ...string)                // CI as given once bs has seen it as it is
	requires(x *toy, names ...string)                        // the checks trunk's protection requires
	behind(x *toy)                                           // trunk moves on elsewhere
	sync(x *toy, dir string)                                 // bring trunk into dir, with a commit
	land(x *toy, n int)                                      // merge it
	close(x *toy, n int)                                     // close it unmerged
	note(x *toy, n int, body string, at time.Time)
	adopt(x *toy, dir string, n int) // point dir at change n's head, committing nothing
	foreign(x *toy, n int)           // its head in a fork that takes no edits
	offline(x *toy, off bool)
	stack(x *toy, dir, name string) // start a stacked change on top of dir's head
	// pullRebase brings other's head (pushed as branch name) under dir's commits, as
	// git pull --rebase does.
	pullRebase(x *toy, dir, other, name string)
	url(n int) string
	other() string // a change URL of another repo
}

// both runs f on each backend.
func both(t *testing.T, f func(t *testing.T, x *toy, b backend)) {
	for name, b := range map[string]func() backend{"github": func() backend { return &ghBackend{} }, "fakecl": func() backend { return &clBackend{} }} {
		t.Run(name, func(t *testing.T) {
			x := newToy(t)
			bk := b()
			bk.setup(x, true)
			f(t, x, bk)
		})
	}
}

type ghBackend struct{}

func (ghBackend) setup(x *toy, remote bool) {
	if remote {
		x.github()
	}
}
func (ghBackend) commit(x *toy, dir string) {
	x.commit(dir, fmt.Sprintf("f%d.txt", time.Now().UnixNano()))
}
func (ghBackend) open(x *toy, n int, dir, head string, draft bool) string {
	x.push(dir, head)
	return x.openPR(n, head, func(pr *fake.PR) { pr.Draft = draft })
}
func (ghBackend) update(x *toy, n int, dir string) {
	x.push(dir, x.gh.PRs[ghBackend{}.url(n)].Head)
}
func (ghBackend) ci(x *toy, n int, buckets ...string) {
	pr := x.gh.PRs[ghBackend{}.url(n)]
	pr.CI = nil
	for i, b := range buckets {
		opt := strings.HasSuffix(b, "?")
		state := map[string]string{"pass": "SUCCESS", "fail": "FAILURE", "pending": "PENDING"}[strings.TrimSuffix(b, "?")]
		pr.CI = append(pr.CI, fake.Check{Name: fmt.Sprintf("job%d", i+1), State: state, Optional: opt})
	}
}
func (ghBackend) ciLater(x *toy, n int, buckets ...string) {
	var states []string
	for _, b := range buckets {
		states = append(states, map[string]string{"pass": "SUCCESS", "fail": "FAILURE", "pending": "PENDING"}[b])
	}
	var next fake.PR
	ci(states...)(&next)
	x.gh.Edit(ghBackend{}.url(n), func(pr *fake.PR) { pr.Next = &next.CI })
}
func (ghBackend) requires(x *toy, names ...string) {
	x.gh.Requires = map[string][]string{"main": names}
}
func (ghBackend) behind(x *toy) {
	x.commit(x.repo, fmt.Sprintf("main%d.txt", time.Now().UnixNano()))
	x.git(x.repo, "push", "-q", "origin", "main")
}
func (ghBackend) sync(x *toy, dir string) {
	x.git(dir, "fetch", "-q", "origin")
	x.git(dir, "merge", "-q", "--no-edit", "origin/main")
}
func (ghBackend) land(x *toy, n int)  { x.mergePR(ghBackend{}.url(n)) }
func (ghBackend) close(x *toy, n int) { x.gh.PRs[ghBackend{}.url(n)].State = "CLOSED" }
func (ghBackend) note(x *toy, n int, body string, at time.Time) {
	pr := x.gh.PRs[ghBackend{}.url(n)]
	pr.Comments = append(pr.Comments, fake.Comment{Author: "jmmshn", Body: body, At: at})
}
func (ghBackend) adopt(x *toy, dir string, n int) {
	x.git(dir, "fetch", "-q", "origin")
	x.git(dir, "reset", "-q", "--hard", "origin/"+x.gh.PRs[ghBackend{}.url(n)].Head)
}
func (ghBackend) foreign(x *toy, n int) {
	pr := x.gh.PRs[ghBackend{}.url(n)]
	pr.Fork, pr.MaintainerCanModify = "someone/toy", false
}
func (ghBackend) offline(x *toy, off bool)       { x.gh.Offline = off }
func (ghBackend) stack(x *toy, dir, name string) { x.git(dir, "checkout", "-q", "-b", name) }
func (ghBackend) pullRebase(x *toy, dir, other, name string) {
	x.push(other, name)
	x.git(dir, "pull", "-q", "--rebase", "origin", name)
}
func (ghBackend) url(n int) string { return fmt.Sprintf("%s/pull/%d", toyRepo, n) }
func (ghBackend) other() string    { return "https://github.com/someone/else/pull/1" }

type clBackend struct{ h *fake.CLHost }

func (b *clBackend) setup(x *toy, remote bool) {
	b.h = &fake.CLHost{}
	if remote {
		b.h.RepoName = "toy"
	}
	x.ports.Review, x.ports.VCS = b.h, b.h
}
func (b *clBackend) commit(_ *toy, dir string) { b.h.Commit(dir) }
func (b *clBackend) open(_ *toy, n int, dir, _ string, draft bool) string {
	return b.h.Upload(dir, n, draft)
}
func (b *clBackend) update(_ *toy, n int, dir string) { b.h.Upload(dir, n, false) }
func (b *clBackend) ci(_ *toy, n int, buckets ...string) {
	var req, opt []port.CICheck
	for i, s := range buckets {
		c := port.CICheck{Name: fmt.Sprintf("job%d", i+1), Bucket: strings.TrimSuffix(s, "?")}
		if strings.HasSuffix(s, "?") {
			opt = append(opt, c)
		} else {
			req = append(req, c)
		}
	}
	b.h.Set(n, func(_ *port.Change, ci *[]port.CICheck) { *ci = req })
	b.h.SetOptional(n, opt)
}
func (b *clBackend) ciLater(_ *toy, n int, buckets ...string) {
	var next []port.CICheck
	for i, s := range buckets {
		next = append(next, port.CICheck{Name: fmt.Sprintf("job%d", i+1), Bucket: s})
	}
	b.h.Next(n, next)
}
func (b *clBackend) requires(_ *toy, names ...string) { b.h.TrunkRequires = names }
func (b *clBackend) behind(*toy)                      { b.h.Trunk() }
func (b *clBackend) sync(_ *toy, dir string)          { b.h.Sync(dir) }
func (b *clBackend) land(_ *toy, n int)               { b.h.Land(n) }
func (b *clBackend) close(_ *toy, n int) {
	b.h.Set(n, func(c *port.Change, _ *[]port.CICheck) { c.State = "closed" })
}
func (b *clBackend) note(_ *toy, n int, body string, at time.Time) {
	b.h.Set(n, func(c *port.Change, _ *[]port.CICheck) { c.Notes = append(c.Notes, port.Note{Body: body, At: at}) })
}
func (b *clBackend) adopt(_ *toy, dir string, n int) {
	var head string
	b.h.Set(n, func(c *port.Change, _ *[]port.CICheck) { head = c.HeadCommit })
	b.h.Move(dir, head)
}
func (b *clBackend) foreign(_ *toy, n int) {
	b.h.Set(n, func(c *port.Change, _ *[]port.CICheck) {
		c.Foreign, c.ForeignRepo, c.Editable = true, "someone/toy", false
	})
}
func (b *clBackend) offline(_ *toy, off bool)                { b.h.Offline = off }
func (b *clBackend) stack(*toy, string, string)              {} // a CL stacks on the workspace's head as it is
func (b *clBackend) pullRebase(_ *toy, dir, other, _ string) { b.h.Rebase(dir, b.h.Head(other)) }
func (b *clBackend) url(n int) string                        { return fmt.Sprintf("https://cl.test/toy/%d", n) }
func (b *clBackend) other() string                           { return "https://cl.test/else/1" }

// --goal pr: no PR is a refusal; each failing check is named at once (exit 7), CI red
// naming its checks; CI pending, or none registered while trunk has checks, is exit 10
// when nothing else failed (bees-13.6: dones sent with CI pending sat red); a vacuous
// pass is printed; bs end
// waits for the merge (exit 9, naming the PR), then lands it, and prune removes it.
func TestPRGoalHandoffChecks(t *testing.T) {
	both(t, func(t *testing.T, x *toy, b backend) {
		ctx := context.Background()
		sp := x.spawn("toy-1", "pr")
		dir := sp.Workspace.Dir
		must(t, x.worker("toy-1", 1, "done"), 7, "no change from toy-1 since this spawn", "next: ")
		b.commit(x, dir)
		url := b.open(x, 1, dir, "toy-1", false)
		b.ci(x, 1, "pass", "fail")
		must(t, x.worker("toy-1", 2, "done"), 7, "1 handoff checks failed", "ci_green", "failing: job2")
		b.ci(x, 1, "pending")
		must(t, x.worker("toy-1", 3, "done"), 10, "still pending", "pending: job1", "next: bs done --wait")
		b.ci(x, 1, "fail?") // only required checks count (keeper, 2026-10-22)
		must(t, x.worker("toy-1", 4, "done"), 0, "handed off to queen-toy", url, "vacuous pass:", "requires no checks")
		b.behind(x)
		must(t, x.keeper("check", "toy-1"), 7, "FAIL pr_current", "1 commits behind")
		must(t, x.queen("end", "toy-1"), 9, url+": open")
		b.sync(x, dir)
		b.update(x, 1, dir)
		must(t, x.keeper("check", "toy-1"), 0, "ok   pr_current")
		b.land(x, 1)
		must(t, x.queen("end", "toy-1"), 0, "ended toy-1")
		if ld, ok, _ := x.open().LandedOf(ctx, sp.ID); !ok || ld.By != "end" || ld.Checks[0] != goal.PRMerged {
			t.Fatalf("landed = %+v, %v", ld, ok)
		}
		must(t, x.queen("prune"), 0, "removed "+dir)
	})
}

// A draft PR fails --goal pr's handoff; two open changes from the workspace are refused,
// naming both: bs judges one (ADR-0010 Amendment 4).
func TestPRGoalRefusesDraftsAndTwoOpen(t *testing.T) {
	both(t, func(t *testing.T, x *toy, b backend) {
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		url := b.open(x, 1, sp.Workspace.Dir, "toy-1", true)
		b.ci(x, 1, "pending")
		must(t, x.worker("toy-1", 1, "done"), 7, "2 handoff checks failed", url+": a draft", "pr_not_draft", "pending: job1")
		two := b.open(x, 2, sp.Workspace.Dir, "toy-1", false)
		must(t, x.worker("toy-1", 2, "done"), 7, "2 open changes from toy-1", url, two, "next: close all but one")
	})
}

// pr:ready completes at handoff; pr:draft takes a draft, needs its CI green like every PR
// goal (keeper, 2026-10-22), completes at handoff, and prunes once its PR is closed
// unmerged, its branch with it.
func TestPRReadyAndDraft(t *testing.T) {
	both(t, func(t *testing.T, x *toy, b backend) {
		ctx := context.Background()
		sp := x.spawn("toy-1", "pr:ready")
		b.commit(x, sp.Workspace.Dir)
		url := b.open(x, 1, sp.Workspace.Dir, "toy-1", false)
		must(t, x.worker("toy-1", 1, "done"), 0)
		if ld, ok, _ := x.open().LandedOf(ctx, sp.ID); !ok || ld.By != "done" {
			t.Fatalf("pr:ready landed = %+v, %v", ld, ok)
		}
		must(t, x.queen("end", "toy-1"), 0)
		must(t, x.queen("prune"), 3, "kept", url+": open") // prune waits for the merge
		b.land(x, 1)
		must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir)

		sp = x.spawn("toy-2", "pr:draft")
		b.commit(x, sp.Workspace.Dir)
		url = b.open(x, 2, sp.Workspace.Dir, "toy-2", true)
		b.ci(x, 2, "fail") // a draft gets no CI check at all (keeper, 2026-10-22)
		must(t, x.worker("toy-2", 1, "done"), 0)
		if d, _, _ := x.open().LatestDone(ctx, sp.ID); slices.ContainsFunc(d.Checks, func(r logbook.CheckResult) bool { return r.Name == goal.CIGreen }) {
			t.Fatalf("a draft's handoff ran ci_green: %+v", d.Checks)
		}
		must(t, x.queen("end", "toy-2"), 0)
		must(t, x.queen("prune"), 3, url+": open")
		b.close(x, 2)
		must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir+" and branch toy-2", "its work is on "+url)
	})
}

// --goal push=<url>: the workspace starts at the target's head on no branch; the handoff
// needs the target's head to descend from a commit made there; bs end waits for its merge;
// prune removes the workspace and has no branch to delete. --goal review=<url>: the
// handoff needs the marker bs:<spawn id> on the target since the spawn.
func TestPushAndReviewGoals(t *testing.T) {
	both(t, func(t *testing.T, x *toy, b backend) {
		ctx := context.Background()
		other := filepath.Join(filepath.Dir(x.repo), "theirs")
		x.git(x.repo, "worktree", "add", "-q", "-b", "feature", other)
		b.commit(x, other)
		target := b.open(x, 5, other, "feature", false)
		head := x.headOf(b, other)

		sp := x.spawn("toy-1", "push="+target)
		dir := sp.Workspace.Dir
		if sp.Workspace.Branch != "" || x.headOf(b, dir) != head {
			t.Fatalf("push workspace %+v at %s, want detached at %s", sp.Workspace, x.headOf(b, dir), head)
		}
		must(t, x.worker("toy-1", 1, "inbox"), 0, "starts at "+target+"'s head")
		must(t, x.worker("toy-1", 2, "done"), 7, "push_moved", "nothing was committed")
		b.commit(x, dir)
		must(t, x.worker("toy-1", 3, "done"), 7, "push_moved", "holds no commit made in "+dir)
		b.update(x, 5, dir)
		must(t, x.worker("toy-1", 4, "done"), 0, "handed off")
		must(t, x.queen("end", "toy-1"), 9, target+": open")
		b.land(x, 5)
		must(t, x.queen("end", "toy-1"), 0)
		must(t, x.queen("prune"), 0, "removed "+dir+" (toy-1)")

		b.commit(x, other)
		target = b.open(x, 6, other, "feature-2", false)
		sp = x.spawn("toy-2", "review="+target)
		mark := "bs:" + string(sp.ID)
		must(t, x.worker("toy-2", 1, "inbox"), 0, mark)
		must(t, x.worker("toy-2", 2, "done"), 7, "review_comment", "carries "+mark)
		b.note(x, 6, "looks fine "+mark, sp.TS.Add(-time.Hour)) // before the spawn: not this review
		b.note(x, 6, "looks fine, no marker", time.Now())
		must(t, x.worker("toy-2", 3, "done"), 7, "review_comment")
		b.note(x, 6, "looks fine\n\n"+mark, time.Now())
		must(t, x.worker("toy-2", 4, "done"), 0)
		if _, ok, _ := x.open().LandedOf(ctx, sp.ID); !ok {
			t.Fatal("review= didn't land at handoff")
		}
	})
}

// Rule G at spawn: a pr goal in a repo on no review host, a target that isn't an open
// change of the project's repo, and a fork's PR that takes no edits are refused (exit 2)
// and record nothing; a host that can't be reached is exit 6, never 2.
func TestSpawnRefusesAPRGoalWithoutARemote(t *testing.T) {
	both(t, func(t *testing.T, x *toy, b backend) {
		other := filepath.Join(filepath.Dir(x.repo), "theirs")
		x.git(x.repo, "worktree", "add", "-q", "-b", "feature", other)
		b.commit(x, other)
		open5 := b.open(x, 5, other, "feature", false)
		b.commit(x, other)
		closed6 := b.open(x, 6, other, "feature", false)
		b.close(x, 6)
		x.bead("toy-1", "x")
		must(t, x.queen("spawn", "--goal", "push="+closed6, "toy-1"), 2, "it is closed, not open")
		must(t, x.queen("spawn", "--goal", "review="+b.other(), "toy-1"), 2, "not of this project's repo")
		must(t, x.queen("spawn", "--goal", "push="+b.url(9), "toy-1"), 2, "no such change")
		b.foreign(x, 5)
		must(t, x.queen("spawn", "--goal", "push="+open5, "toy-1"), 2, "doesn't let this project push to it")
		must(t, x.queen("spawn", "--goal", "review="+open5, "toy-1"), 0) // a review pushes nothing
		x.bead("toy-2", "x")
		b.offline(x, true)
		must(t, x.queen("spawn", "--goal", "review="+open5, "toy-2"), 6, "can't ask")
		b.offline(x, false)
		if _, ok, _ := x.open().LatestSpawn(context.Background(), "toy-2"); ok {
			t.Fatal("a refused spawn was recorded")
		}
	})
	t.Run("no remote", func(t *testing.T) {
		for _, b := range []backend{&ghBackend{}, &clBackend{}} {
			x := newToy(t)
			b.setup(x, false)
			x.bead("toy-1", "x")
			must(t, x.queen("spawn", "--goal", "pr", "toy-1"), 2, "belongs to no", "repo")
		}
	})
}

// headOf is dir's head commit as backend b's VCS sees it.
func (x *toy) headOf(b backend, dir string) string {
	if cl, ok := b.(*clBackend); ok {
		return cl.h.Head(dir)
	}
	return x.git(dir, "rev-parse", "HEAD")
}

// bs done --wait (keeper, bees-13.6): on CI still running bs itself asks again until it
// finishes, then hands off on green or names what failed; past --timeout it is exit 11.
// bs check says pending with exit 10 too.
func TestDoneWaitsForCI(t *testing.T) {
	both(t, func(t *testing.T, x *toy, b backend) {
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		url := b.open(x, 1, sp.Workspace.Dir, "toy-1", false)
		b.ci(x, 1, "pending")
		must(t, x.worker("toy-1", 1, "done"), 10, "next: bs done --wait")
		must(t, x.worker("toy-1", 2, "done", "--wait", "--timeout", "1s"), 11, "still pending after 1s", "pending: job1")
		must(t, x.worker("toy-1", 3, "done", "--wait", "--timeout", "20m"), 2, "at most 9m0s")
		b.ciLater(x, 1, "pass", "fail")
		must(t, x.worker("toy-1", 4, "done", "--wait"), 7, "failing: job2", "still pending (")
		b.ci(x, 1, "pending")
		b.ciLater(x, 1, "pass")
		must(t, x.worker("toy-1", 5, "handoff"), 0, "handed off to queen-toy", url) // handoff is done --wait
		b.ci(x, 1, "pending")
		must(t, x.keeper("check", "toy-1"), 10, "WAIT ci_green", "pending: job1")
	})
}

// A host that hangs is a check that couldn't run (exit 6), even under --wait: exit 11 is
// only for pending that outlived the wait (eng-review of #108).
func TestAHungHostIsCantCheckNotPending(t *testing.T) {
	x := newToy(t)
	b := ghBackend{}
	b.setup(x, true)
	sp := x.spawn("toy-1", "pr")
	b.commit(x, sp.Workspace.Dir)
	b.open(x, 1, sp.Workspace.Dir, "toy-1", false)
	x.gh.Hang = true
	must(t, x.worker("toy-1", 1, "done", "--wait", "--timeout", "1s"), 6, "can't ask github")
}

// A waiver holds at end and prune as at handoff; an amend re-runs the spawn refusals on
// either host.
func TestWaiversHoldAtEndAndAmendsAreRefused(t *testing.T) {
	both(t, func(t *testing.T, x *toy, b backend) {
		ctx := context.Background()
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		url := b.open(x, 1, sp.Workspace.Dir, "toy-1", false)
		must(t, x.worker("toy-1", 1, "done"), 0)
		// An amend tells the worker its new goal's fragments, and a review's marker.
		must(t, x.keeper("goal", "toy-1", "pr", "review="+url), 0)
		must(t, x.worker("toy-1", 2, "inbox"), 0, "now --goal pr review="+url, "bs:"+string(sp.ID), "## Goal", "--goal review=<url>")
		must(t, x.keeper("goal", "toy-1", "push="+url), 2, "starts at its target's head")
		must(t, x.keeper("goal", "toy-1", "pr"), 0)
		b.close(x, 1)
		must(t, x.keeper("goal", "toy-1", "push="+url), 2, "bs goal: --goal push="+url, "not open")
		must(t, x.queen("end", "toy-1"), 9, "closed")
		must(t, x.keeper("goal", "toy-1", "--waive", "pr_merged"), 0, "(waived: pr_merged)")
		must(t, x.queen("end", "toy-1"), 0)
		if ld, ok, _ := x.open().LandedOf(ctx, sp.ID); !ok || len(ld.Checks) != 0 {
			t.Fatalf("landed = %+v, %v; want landed with no complete check run", ld, ok)
		}
	})
}

// A mixed goal (push=<url> plus pr) holds every part: the worker's own branch is cut at the
// push target's head, its commits must reach the target, and its own PR must pass too.
func TestAMixedGoalHoldsEveryPart(t *testing.T) {
	x := newToy(t)
	b := ghBackend{}
	b.setup(x, true)
	other := filepath.Join(filepath.Dir(x.repo), "theirs")
	x.git(x.repo, "worktree", "add", "-q", "-b", "feature", other)
	b.commit(x, other)
	target := b.open(x, 5, other, "feature", false)
	sp := x.spawn("toy-1", "push="+target, "pr")
	dir := sp.Workspace.Dir
	if sp.Workspace.Branch != "toy-1" || x.headOf(b, dir) != x.git(other, "rev-parse", "HEAD") {
		t.Fatalf("mixed workspace %+v not cut at the target's head", sp.Workspace)
	}
	must(t, x.worker("toy-1", 1, "inbox"), 0, "starts at "+target+"'s head", "git push origin HEAD:feature")
	b.commit(x, dir)
	b.open(x, 6, dir, "toy-1", false)
	must(t, x.worker("toy-1", 2, "done"), 7, "push_moved")
	b.update(x, 5, dir)
	must(t, x.worker("toy-1", 3, "done"), 0)
	b.land(x, 6)
	must(t, x.queen("end", "toy-1"), 9, target+": open")
	b.land(x, 5)
	must(t, x.queen("end", "toy-1"), 0)
}

// GitHub lags a push by seconds (seen live): a branch pushed to its upstream that the PR
// doesn't show yet is pending (exit 10, --wait rides it out), never "push it"; one never
// pushed still is; a PR head holding commits the branch lacks says pull, never pending
// (a --wait on it would never end).
func TestAPushTheHostHasntSeenYetIsPending(t *testing.T) {
	x := newToy(t)
	b := ghBackend{}
	b.setup(x, true)
	sp := x.spawn("toy-1", "pr")
	dir := sp.Workspace.Dir
	b.commit(x, dir)
	url := b.open(x, 1, dir, "toy-1", false)
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.gh.Edit(url, func(pr *fake.PR) { pr.Stale = true })
	b.commit(x, dir)
	x.git(dir, "push", "-q", "-u", "origin", "toy-1")
	must(t, x.worker("toy-1", 2, "done"), 10, "pending: toy-1 was pushed at", "still shows")
	time.AfterFunc(300*time.Millisecond, func() { x.gh.Edit(url, func(pr *fake.PR) { pr.Stale = false }) })
	must(t, x.worker("toy-1", 3, "done", "--wait"), 0)
	b.commit(x, dir)
	must(t, x.worker("toy-1", 4, "done", "--wait"), 7, "push it")

	// Someone pushed onto the PR (or "Update branch"): the host is ahead, never pending.
	x.git(dir, "push", "-q", "origin", "toy-1")
	other := filepath.Join(filepath.Dir(x.repo), "helper")
	x.git(x.repo, "fetch", "-q", "origin")
	x.git(x.repo, "worktree", "add", "-q", "--detach", other, "origin/toy-1")
	b.commit(x, other)
	x.push(other, "toy-1")
	must(t, x.worker("toy-1", 5, "done", "--wait", "--timeout", "1s"), 7, "has commits toy-1 lacks", "pull them, then push")
}

// While the host still shows the old head of a pushed branch, what it says of that head
// (CI red, behind) is about a commit the worker has moved past: pr_current and ci_green are
// pending too, so a fix pushed after red CI waits (exit 10), never fails at once on the
// old run (eng-review of #108). A push= target the host lags on is pending the same way.
func TestTheOldHeadsCIIsPendingWhileTheHostLags(t *testing.T) {
	x := newToy(t)
	b := ghBackend{}
	b.setup(x, true)
	sp := x.spawn("toy-1", "pr")
	dir := sp.Workspace.Dir
	b.commit(x, dir)
	url := b.open(x, 1, dir, "toy-1", false)
	b.ci(x, 1, "fail")
	must(t, x.worker("toy-1", 1, "done"), 7, "failing: job1")
	x.gh.Edit(url, func(pr *fake.PR) { pr.Stale = true })
	b.behind(x)
	b.commit(x, dir)
	x.git(dir, "push", "-q", "-u", "origin", "toy-1")
	must(t, x.worker("toy-1", 2, "done"), 10, "pending: toy-1 was pushed at", "ci_green", "pr_current")

	other := filepath.Join(filepath.Dir(x.repo), "theirs")
	x.git(x.repo, "worktree", "add", "-q", "-b", "feature", other)
	b.commit(x, other)
	target := b.open(x, 5, other, "feature", false)
	b.ci(x, 5, "pass")
	sp = x.spawn("toy-2", "push="+target)
	dir = sp.Workspace.Dir
	b.commit(x, dir)
	x.gh.Edit(target, func(pr *fake.PR) { pr.Stale = true })
	b.update(x, 5, dir)
	must(t, x.worker("toy-2", 1, "done"), 10, "push_moved", "pending: feature was pushed at")
	time.AfterFunc(300*time.Millisecond, func() { x.gh.Edit(target, func(pr *fake.PR) { pr.Stale = false }) })
	must(t, x.worker("toy-2", 2, "done", "--wait"), 0)
}

// A pull brings in work another worker opened a change for, which is never this worker's:
// bs finds the change from the worker's own branch only (ADR-0010 Amendment 4), so the
// other one is never judged, and after a pull --rebase the worker's own passes.
func TestAPulledCommitIsNeverTheWorkers(t *testing.T) {
	both(t, func(t *testing.T, x *toy, b backend) {
		sp := x.spawn("toy-1", "pr")
		dir := sp.Workspace.Dir
		b.commit(x, dir)
		written := x.headOf(b, dir)
		other := filepath.Join(filepath.Dir(x.repo), "theirs")
		x.git(x.repo, "worktree", "add", "-q", "-b", "theirs", other)
		b.commit(x, other)
		b.pullRebase(x, dir, other, "theirs")
		if x.headOf(b, dir) == written {
			t.Fatal("the pull didn't rebase the worker's commit")
		}
		theirs := b.open(x, 2, other, "theirs", false)
		r := x.worker("toy-1", 1, "done")
		must(t, r, 7, "no change from toy-1")
		lacks(t, r, theirs)
		url := b.open(x, 1, dir, "toy-1", false)
		must(t, x.worker("toy-1", 2, "done"), 0, "handed off", url)
	})
}

// Right after a push the host lists no checks yet: a check the base's protection requires
// that CI hasn't reported is pending, never "none required" (keeper, 2026-10-22).
func TestARequiredCheckNotReportedYetIsPending(t *testing.T) {
	both(t, func(t *testing.T, x *toy, b backend) {
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		b.open(x, 1, sp.Workspace.Dir, "toy-1", false)
		b.requires(x, "job1")
		must(t, x.worker("toy-1", 1, "done"), 10, "pending: job1 (not reported yet)")
		b.ci(x, 1, "pending")
		b.ciLater(x, 1, "pass")
		must(t, x.worker("toy-1", 2, "handoff"), 0, "handed off")
	})
}
