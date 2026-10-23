package core

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/prompt"
)

// SpawnReq is what bs spawn takes from its caller: only what bs can't look up (Rule D).
type SpawnReq struct {
	By      string // the queen
	Session string // the herdr session the spawn opens on: the queen's
	Bead    string
	Label   string // "" is the bead's title
	Goal    string // --goal, required
}

// Spawn starts a worker for req.Bead: it resolves the goal, opens its git_worktree
// workspace through herdr, records the spawn with its brief, launches the harness, and
// confirms the start. Nothing is recorded until the workspace exists; a launch that fails
// after the record leaves an unconfirmed spawn (exit 8) that bs end clears. An open spawn
// whose process died is resumed instead (resumed true).
func (c *Core) Spawn(ctx context.Context, req SpawnReq) (sp logbook.Spawn, resumed bool, err error) {
	g, err := goal.Parse(req.Goal)
	if err != nil {
		return logbook.Spawn{}, false, exit(ExitRefused, "", err, "%s", req.Bead)
	}
	g.Fragment = prompt.Hash(g.Type)
	bead, err := c.Beads.Show(ctx, req.Bead)
	if err != nil {
		if errors.Is(err, port.ErrNoBead) {
			return logbook.Spawn{}, false, exit(ExitRefused, "bd show "+req.Bead, err, "no bead %s", req.Bead)
		}
		return logbook.Spawn{}, false, exit(ExitCantCheck, "", err, "can't read bead %s", req.Bead)
	}
	name := Name(bead.ID)
	var ended *logbook.Spawn // the last spawn, ended: its kept worktree and branch are reused
	switch prev, ok, err := c.Logbook.LatestSpawn(ctx, name); {
	case err != nil:
		return logbook.Spawn{}, false, err
	case ok && !prev.Ended:
		return prev, true, c.resume(ctx, prev, bead, req)
	case ok:
		ended = &prev
	}
	at, err := c.beadAt(ctx, bead)
	if err != nil {
		return logbook.Spawn{}, false, err
	}
	target, err := c.resolveReview(ctx, &g)
	if err != nil {
		return logbook.Spawn{}, false, err
	}
	ws, reused, err := c.workspace(ctx, name, target != nil, ended)
	if err != nil {
		return logbook.Spawn{}, false, err
	}
	if err := c.refuse(g, ws); err != nil {
		return logbook.Spawn{}, false, err
	}
	deny, err := c.deny(ctx)
	if err != nil {
		return logbook.Spawn{}, false, err
	}
	// Prepare is pure, so it runs before anything is made: a refused launch leaves nothing.
	launch, err := c.Harness.Prepare(c.workerStart(name, ws, doorbell(name, bead.ID, req.By), deny))
	if err != nil {
		return logbook.Spawn{}, false, exit(ExitRefused, "", err, "%s: its harness refused the launch; nothing was made", name)
	}
	text := prompt.Text(prompt.Worker, c.Project, ws.Kind, g.Types())
	if g.Reviewed() {
		text += prompt.Host(prompt.Worker, c.Review.Name())
	}
	launch.Files = withPrompt(launch.Files, text)
	label := cmp.Or(req.Label, bead.Title)
	session := cmp.Or(req.Session, logbook.DefaultSession)
	var hw herdr.Workspace
	remade := false // a reused branch whose worktree was gone, checked out again here
	if reused {
		if _, err := os.Stat(ws.Dir); errors.Is(err, os.ErrNotExist) {
			if _, err := c.git(ctx, ws.Repo, "worktree", "add", ws.Dir, ws.Branch); err != nil {
				return logbook.Spawn{}, false, exit(ExitFail, "", err, "%s: couldn't check out branch %s again at %s; nothing was recorded", name, ws.Branch, ws.Dir)
			}
			remade = true
		}
		if hw, err = c.herdr(session).CreateWorkspace(ctx, ws.Dir, label); err != nil {
			if remade {
				_, rerr := c.git(context.WithoutCancel(ctx), ws.Repo, "worktree", "remove", ws.Dir)
				err = errors.Join(err, rerr)
			}
			return logbook.Spawn{}, false, exit(ExitFail, "", err, "%s: herdr couldn't open a workspace on its kept worktree; nothing was recorded", name)
		}
		if remade {
			c.warn("%s: on branch %s its last spawn left, checked out again at %s", name, ws.Branch, ws.Dir)
		} else {
			c.warn("%s: on its kept worktree and branch %s, with the work its last spawn left there", name, ws.Branch)
		}
	} else if hw, err = c.makeWorkspace(ctx, session, &ws, target, label); err != nil {
		return logbook.Spawn{}, false, err
	}
	// undo takes back what this spawn made: a reused branch stays, and its worktree unless
	// this spawn checked it out again.
	undo := func() error {
		if reused {
			err := c.herdr(session).CloseWorkspace(context.WithoutCancel(ctx), hw.ID)
			if remade {
				_, rerr := c.git(context.WithoutCancel(ctx), ws.Repo, "worktree", "remove", ws.Dir)
				err = errors.Join(err, rerr)
			}
			return err
		}
		return c.unmake(context.WithoutCancel(ctx), session, hw.ID, ws)
	}
	// Where the branch starts: a pr goal's change must be ahead of it (pr_head).
	if ws.Branch != "" {
		tip, ok, err := c.VCS.Branch(ctx, ws.Dir, ws.Branch)
		if err == nil && !ok {
			err = fmt.Errorf("no branch %s in %s", ws.Branch, ws.Dir)
		}
		if err != nil {
			err = errors.Join(err, undo())
			return logbook.Spawn{}, false, exit(ExitFail, "", err, "%s: can't read where its branch starts; nothing was recorded", name)
		}
		ws.Start = tip
	}
	// The worker= tag names whose workspace this is to anyone reading herdr's list.
	if err := c.herdr(session).TagWorkspace(ctx, hw.ID, map[string]string{"worker": name}); err != nil {
		err = errors.Join(err, undo())
		return logbook.Spawn{}, false, exit(ExitFail, "", err, "%s: herdr couldn't tag its workspace; nothing was recorded", name)
	}
	sp = logbook.Spawn{
		Name: name, By: req.By, Bead: bead.ID, Label: label, Harness: c.HarnessName,
		Herdr: logbook.Pane{Session: session, Workspace: hw.ID, Pane: hw.Pane}, Workspace: ws,
		Goal: g, ConfigDir: c.ConfigDir, At: at, PruneIgnore: c.PruneIgnore,
	}
	sp.Record = launch.Record
	if sp.ID, err = c.Logbook.RecordSpawn(ctx, req.By, sp, brief(bead, g, target, c.Guards.AllowMergeInto)); err != nil {
		err = errors.Join(err, undo())
		if errors.Is(err, logbook.ErrAlreadySpawned) {
			return logbook.Spawn{}, false, exit(ExitRefused, bsname.Cmd+" end "+name, err, "another spawn of %s was recorded first", name)
		}
		return logbook.Spawn{}, false, exit(ExitFail, "", err, "%s: couldn't record the spawn", name)
	}
	return sp, false, c.launch(ctx, sp, launch, "")
}

// resume restarts open spawn sp whose recorded process died, in its recorded pane, with
// its recorded conversation once it has one (it read its inbox), and records the new
// start as replacing the dead one (bees-13.27; ADR-0009: resume the recorded conversation
// or fail visibly). A running worker, a goal other than the recorded one, or a pane herdr
// no longer has is refused with the next step; the workspace and record stay as they are.
func (c *Core) resume(ctx context.Context, sp logbook.Spawn, bead port.Bead, req SpawnReq) error {
	e, err := c.Logbook.Goal(ctx, sp)
	if err != nil {
		return err
	}
	if g, err := goal.Parse(req.Goal); err == nil && g.String() != e.String() {
		return exit(ExitRefused, bsname.Cmd+" goal "+sp.Name+" "+req.Goal, nil,
			"%s is spawned and not ended, and it was spawned with --goal %s, not --goal %s; a resume keeps its goal", sp.Name, e.Goal, g)
	}
	st, started, err := c.StartOf(ctx, sp)
	if err != nil {
		return exit(ExitCantCheck, "", err, "%s: can't tell whether it still runs; nothing started", sp.Name)
	}
	if started {
		switch err := c.running(st.Process); {
		case err == nil:
			return exit(ExitRefused, bsname.Cmd+" send "+sp.Name+" \"...\"", nil, "%s is already running (pid %d)", sp.Name, st.PID)
		case !errors.Is(err, port.ErrNotRunning):
			return exit(ExitCantCheck, "", err, "%s: can't tell whether pid %d still runs; nothing started", sp.Name, st.PID)
		}
	}
	// launch starts the configured harness: a spawn recorded on another is refused, never
	// restarted on a harness that can't resume its conversation (ADR-0010's switch refusal).
	if sp.Harness != c.HarnessName {
		return exit(ExitRefused, bsname.Cmd+" end --incomplete "+sp.Name+", then "+bsname.Cmd+" spawn "+goalFlags(e.Goal)+" "+bead.ID, nil,
			"%s was spawned on harness %s, and this bs starts %s; nothing started", sp.Name, sp.Harness, c.HarnessName)
	}
	if _, err := c.herdr(sp.Herdr.Session).ProcessInfo(ctx, sp.Herdr.Pane); errors.Is(err, herdr.ErrPaneGone) {
		return exit(ExitRefused, bsname.Cmd+" end --incomplete "+sp.Name+", then "+bsname.Cmd+" spawn "+goalFlags(e.Goal)+" "+bead.ID, nil,
			"%s: its pane %s is gone from herdr session %s, so there is nowhere to resume it; nothing started", sp.Name, sp.Herdr.Pane, sp.Herdr.Session)
	} else if err != nil {
		return exit(ExitCantCheck, "", err, "%s: can't look at its pane %s; nothing started", sp.Name, sp.Herdr.Pane)
	}
	deny, err := c.deny(ctx)
	if err != nil {
		return err
	}
	st0 := c.workerStart(sp.Name, sp.Workspace, doorbell(sp.Name, bead.ID, sp.By), deny)
	st0.Continue, st0.Record = sp.Read, sp.Record
	launch, err := c.Harness.Prepare(st0)
	if err != nil {
		return exit(ExitRefused, "", err, "%s: its harness refused the restart; nothing started", sp.Name)
	}
	text := prompt.Text(prompt.Worker, c.Project, sp.Workspace.Kind, e.Types())
	if e.Reviewed() {
		text += prompt.Host(prompt.Worker, c.Review.Name())
	}
	launch.Files = withPrompt(launch.Files, text)
	var replaces logbook.ID
	if started {
		replaces = st.ID
	}
	return c.launch(ctx, sp, launch, replaces)
}

// kept reports whether ended, a worker's last spawn, left its git_worktree branch for a
// new spawn to build on: dir still checked out on it, or dir gone and the branch kept (the
// old bs's end removed the worktree and kept the branch: the rollout's in-flight workers).
// A git probe that couldn't run is an error, never "not kept" (Rule G).
func (c *Core) kept(ctx context.Context, ended *logbook.Spawn, dir string) (bool, error) {
	if ended == nil || ended.Workspace.Kind != config.GitWorktree || ended.Workspace.Branch == "" || ended.Workspace.Dir != dir {
		return false, nil
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return c.hasBranch(ctx, ended.Workspace)
	} else if err != nil {
		return false, err
	}
	head, err := c.git(ctx, dir, "symbolic-ref", "--short", "HEAD")
	var ee *port.ExitError
	if errors.As(err, &ee) {
		return false, nil // not a worktree on a branch any more: the plain refusals say what
	} else if err != nil {
		return false, err
	}
	return head == ended.Workspace.Branch, nil
}

// goalFlags is "--goal <goal>": a next: that spawns again names the goal; a mixed goal's
// first part, since a spawn takes one.
func goalFlags(g goal.Goal) string {
	if g.Mixed != nil {
		return "--goal " + g.Mixed[0]
	}
	return "--goal " + g.String()
}

// workerStart is the start of worker name in dir that every launch of it prepares, a first
// spawn's and a resume's alike: one place for its workspace, prompt file, PATH, guards and
// shell timeout.
func (c *Core) workerStart(name string, ws logbook.Workspace, bell string, deny []string) port.Start {
	return port.Start{Role: port.Worker, Name: name, Dir: ws.Dir, Branch: ws.Branch, Repo: ws.Repo, PromptFile: "{{prompt.md}}", Doorbell: bell,
		Path: c.WorkerPath, Deny: deny, ShellTimeout: ShellTimeout}
}

// refuse runs the spawn-time refusals of goal g that need no network, in workspace ws,
// for bs spawn and every bs goal amend.
func (c *Core) refuse(g goal.Goal, ws logbook.Workspace) error {
	if (g.Type == "branch" || g.Type == "pr") && ws.Branch == "" {
		return exit(ExitRefused, "", nil, "--goal %s: a %s workspace with no branch has none to hand off", g, ws.Kind)
	}
	return nil
}

// beadAt is bead as spawn finds it: the hash of its notes and design, and the beads under
// it. Every spawn freezes it, whatever its goal, so an amend to note or beads checks
// against the spawn.
func (c *Core) beadAt(ctx context.Context, bead port.Bead) (*logbook.BeadAt, error) {
	kids, err := c.Beads.Children(ctx, bead.ID)
	if err != nil {
		return nil, exit(ExitCantCheck, "bd list --parent "+bead.ID, err, "can't list the beads under %s", bead.ID)
	}
	return &logbook.BeadAt{Note: noteHash(bead), Children: kids}, nil
}

// noteHash is the sha256 of b's notes and design, hex: what a note goal must change. A
// label or status change leaves it alone.
func noteHash(b port.Bead) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.Notes+"\x00"+b.Design)))
}

// target is a push goal's change as spawn found it: the workspace starts at its head,
// detached.
type target struct {
	c    port.Change
	hint string // how to push onto it
}

// resolveReview runs the spawn-time refusals of a pr, push or review goal g (Rule G), for
// bs spawn and every bs goal amend: the project's checkout must
// belong to a repo on the review host, which g freezes; a push or review target
// must be an open change of that repo, recorded in its one spelling; a push target whose
// head lives elsewhere (a fork) must let the project push to it. A host that couldn't be
// asked (network, auth) is exit 6, never 2. It returns the push target a workspace starts
// at, if any.
func (c *Core) resolveReview(ctx context.Context, g *goal.Goal) (*target, error) {
	if !g.Reviewed() {
		return nil, nil
	}
	repo, ok, err := c.Review.Repo(ctx, c.Repo)
	switch {
	case err != nil:
		return nil, exit(ExitCantCheck, "", err, "--goal %s: can't tell which %s repo %s belongs to", g, c.Review.Name(), c.Repo)
	case !ok:
		return nil, exit(ExitRefused, "add a remote for it (review.host = "+c.Review.Name()+")", nil,
			"--goal %s: %s belongs to no %s repo, so none of its changes can be checked", g, c.Repo, c.Review.Name())
	}
	g.Repo = repo
	if g.Target == "" {
		return nil, nil
	}
	r, err := c.Review.Parse(g.Target)
	if err != nil {
		return nil, exit(ExitRefused, "", err, "--goal %s", g)
	}
	if !strings.EqualFold(r.Repo, repo) {
		return nil, exit(ExitRefused, "", nil, "--goal %s: that is a change of %s, not of this project's repo %s", g, r.Repo, repo)
	}
	v, err := c.Review.View(ctx, c.Repo, r)
	switch {
	case errors.Is(err, port.ErrNoChange):
		return nil, exit(ExitRefused, "", nil, "--goal %s: %s has no such change", g, repo)
	case err != nil:
		return nil, exit(ExitCantCheck, "", err, "--goal %s: can't ask %s about it", g, c.Review.Name())
	case v.State != port.Open:
		return nil, exit(ExitRefused, "", nil, "--goal %s: it is %s, not open", g, v.State)
	case g.Type == "push" && !v.Editable:
		return nil, exit(ExitRefused, "ask its author to allow edits by maintainers", nil,
			"--goal %s: its head is in %s, which doesn't let this project push to it", g, v.ForeignRepo)
	}
	g.Target = r.URL
	if g.Type != "push" {
		return nil, nil
	}
	return &target{c: v, hint: c.Review.PushHint(ctx, c.Repo, v)}, nil
}

// unmake takes back the worktree and branch a spawn made but couldn't record, or a second
// spawn would find them with nothing to prune them (motif 8). The branch is the spawn's
// own: workspace refused one that already existed.
func (c *Core) unmake(ctx context.Context, session, workspace string, ws logbook.Workspace) error {
	if ws.Branch == "" { // a detached worktree bs made itself, and a workspace on it
		return errors.Join(c.herdr(session).CloseWorkspace(ctx, workspace), c.removeDetached(ctx, ws))
	}
	if err := c.herdr(session).RemoveWorktree(ctx, workspace, true); err != nil {
		return fmt.Errorf("and its worktree %s stays: %w", ws.Dir, err)
	}
	switch ok, err := c.hasBranch(ctx, ws); {
	case err != nil:
		return fmt.Errorf("and can't tell whether its branch %s stays: %w", ws.Branch, err)
	case ok:
		if _, err := c.git(ctx, ws.Repo, "branch", "-D", ws.Branch); err != nil {
			return fmt.Errorf("and its branch %s stays: %w", ws.Branch, err)
		}
	}
	return nil
}

// workspace works out the spawn's workspace (Rule D): the project dir the top level of a
// git checkout is git_worktree, on a branch of its own, or detached for a push goal (none
// and command arrive with bees-13.10 and 13.11). When ended, name's last spawn, left its
// branch (its worktree kept, or gone with the branch kept), it is reused (reused true): the
// work there is the new spawn's to build on, never refused with no way on (motif 5).
func (c *Core) workspace(ctx context.Context, name string, detached bool, ended *logbook.Spawn) (ws logbook.Workspace, reused bool, err error) {
	if c.Repo == "" {
		return logbook.Workspace{}, false, exit(ExitRefused, "", nil, "project %s has no repo: run it in the repo, or set paths.repo", c.Project)
	}
	top, err := c.git(ctx, c.Repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return logbook.Workspace{}, false, refusedOrCant(err, "", "%s is not a git checkout (workspace none arrives with bees-13.10)", c.Repo)
	}
	real, err := filepath.EvalSymlinks(c.Repo)
	if err != nil {
		return logbook.Workspace{}, false, exit(ExitCantCheck, "", err, "can't resolve paths.repo")
	}
	if top != c.Repo && top != real {
		return logbook.Workspace{}, false, exit(ExitRefused, "", nil, "%s is inside the checkout %s, not its top level; set paths.repo to it", c.Repo, top)
	}
	// The checks above say what to fix when the kind was worked out as none; this one
	// names a kind set by hand.
	if c.Kind != "" && c.Kind != "git_worktree" {
		return logbook.Workspace{}, false, exit(ExitRefused, bsname.Cmd+" config", nil, "workspace kind %s (%s) isn't built yet (none: bees-13.10, command: bees-13.11)", c.Kind, c.KindWhy)
	}
	base, err := c.git(ctx, c.Repo, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return logbook.Workspace{}, false, refusedOrCant(err, "git -C "+c.Repo+" switch main", "%s has no branch checked out to cut from", c.Repo)
	}
	dir := filepath.Join(c.Worktrees, name)
	if ok, err := c.kept(ctx, ended, dir); err != nil {
		return logbook.Workspace{}, false, exit(ExitCantCheck, "", err, "can't tell whether %s is %s's kept worktree", dir, name)
	} else if ok && !detached {
		return ended.Workspace, true, nil
	}
	if _, err := os.Stat(dir); err == nil {
		return logbook.Workspace{}, false, exit(ExitRefused, bsname.Cmd+" prune", nil, "%s already exists", dir)
	}
	if detached { // a push goal: the target's head, never a branch of its own
		return logbook.Workspace{Kind: config.GitWorktree, Dir: dir, Repo: c.Repo}, false, nil
	}
	ws = logbook.Workspace{Kind: "git_worktree", Dir: dir, Repo: c.Repo, Branch: name, Base: base}
	// The branch is the spawn's own, whatever its goal, so prune may delete it later: one
	// left by an earlier spawn holds work bs would build on unseen (motif 12).
	switch ok, err := c.hasBranch(ctx, ws); {
	case err != nil:
		return logbook.Workspace{}, false, exit(ExitCantCheck, "", err, "can't tell whether branch %s exists", name)
	case ok:
		// -d, not -D: git refuses to delete a branch whose work isn't merged.
		return logbook.Workspace{}, false, exit(ExitRefused, bsname.Cmd+" prune (it deletes a merged worker's branch), or git -C "+c.Repo+" branch -d "+name, nil,
			"%s already has a branch %s", c.Repo, name)
	}
	return ws, false, nil
}

// writeFiles writes l.Files to the cache dir as <name>-<key>-* (0600), replacing each
// {{key}} in l.Args with its path, after removing this session's earlier files.
func (c *Core) writeFiles(name string, l port.Launch) ([]string, error) {
	args := slices.Clone(l.Args)
	if len(l.Files) == 0 {
		return args, nil
	}
	if err := c.removeFiles(name); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.CacheDir, 0o700); err != nil {
		return nil, err
	}
	for _, key := range slices.Sorted(maps.Keys(l.Files)) {
		stem, ext := key, filepath.Ext(key)
		stem = strings.TrimSuffix(stem, ext)
		f, err := os.CreateTemp(c.CacheDir, name+"-"+strings.ReplaceAll(stem, "_", "-")+"-*"+ext)
		if err != nil {
			return nil, err
		}
		_, err = f.WriteString(l.Files[key])
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
		for i := range args {
			args[i] = strings.ReplaceAll(args[i], "{{"+key+"}}", f.Name())
		}
	}
	return args, nil
}

func brief(b port.Bead, g goal.Goal, t *target, mergeInto []string) string {
	s := fmt.Sprintf("You own %s: %s\n\nYour goal: --goal %s. Hand it off with "+bsname.Cmd+" done when it holds.\n", b.ID, b.Title, g)
	if len(mergeInto) > 0 { // the guard can't see a PR's base: the brief says which (guards.allow_merge_into)
		s += fmt.Sprintf("The keeper lets you merge your own change when its base matches %s; check its base first, and never merge into the default branch.\n", strings.Join(mergeInto, " or "))
	}
	switch {
	case g.Type == "push" && t != nil:
		s += fmt.Sprintf("Your workspace starts at %s's head (%s). Push your commits for it onto it: %s\n", g.Target, short(t.c.HeadCommit), t.hint)
	}
	return s + "\n" + b.Description
}

// doorbell is a worker's first prompt: who it is, and where to read and reply.
func doorbell(name, bead, queen string) string {
	return fmt.Sprintf(`[%s] you are %s, working on %s — run: %s inbox   (reply with: %s send %s "...")`, bsname.Cmd, name, bead, bsname.Cmd, bsname.Cmd, queen)
}

// isExit reports whether err is a tool that ran and exited with code.
func isExit(err error, code int) bool {
	var ee *port.ExitError
	return errors.As(err, &ee) && ee.Code == code
}

// refusedOrCant is a refusal (exit 2) when the tool ran and said no, and "can't check"
// (exit 6) when it couldn't run: a tool that is down is never read as "no" (Rule G).
func refusedOrCant(err error, next, format string, a ...any) *Exit {
	var ee *port.ExitError
	if errors.As(err, &ee) {
		return exit(ExitRefused, next, err, format, a...)
	}
	return exit(ExitCantCheck, "", err, format, a...)
}

// makeWorkspace makes ws and opens a herdr workspace on it: herdr cuts a branch's
// worktree; a push goal's checkout at its target's head the VCS makes (herdr takes no
// --detach), once the review host has fetched that head. ws.Dir becomes
// herdr's answer. Nothing is recorded yet: a failure leaves nothing behind.
func (c *Core) makeWorkspace(ctx context.Context, session string, ws *logbook.Workspace, t *target, label string) (herdr.Workspace, error) {
	name := filepath.Base(ws.Dir)
	if t == nil {
		hw, err := c.herdr(session).CreateWorktree(ctx, ws.Repo, ws.Base, ws.Branch, ws.Dir, label)
		if err != nil {
			return hw, exit(ExitFail, "", err, "%s: herdr couldn't make its worktree; nothing was recorded", name)
		}
		ws.Dir = hw.Path // herdr makes it where asked; its answer is the record
		return hw, nil
	}
	if err := c.Review.Fetch(ctx, ws.Repo, t.c); err != nil {
		return herdr.Workspace{}, exit(ExitCantCheck, "", err, "%s: can't fetch %s's head; nothing was made", name, t.c.URL)
	}
	if err := c.VCS.Detach(ctx, ws.Repo, ws.Dir, t.c.HeadCommit); err != nil {
		return herdr.Workspace{}, exit(ExitFail, "", err, "%s: couldn't make its workspace at %s; nothing was recorded", name, short(t.c.HeadCommit))
	}
	hw, err := c.herdr(session).CreateWorkspace(ctx, ws.Dir, label)
	if err != nil {
		err = errors.Join(err, c.removeDetached(context.WithoutCancel(ctx), *ws))
		return hw, exit(ExitFail, "", err, "%s: herdr couldn't open a workspace on its worktree; nothing was recorded", name)
	}
	return hw, nil
}

// removeDetached removes the detached checkout makeWorkspace made.
func (c *Core) removeDetached(ctx context.Context, ws logbook.Workspace) error {
	if err := c.VCS.Drop(ctx, ws.Repo, ws.Dir); err != nil {
		return fmt.Errorf("and its workspace %s stays: %w", ws.Dir, err)
	}
	return nil
}
