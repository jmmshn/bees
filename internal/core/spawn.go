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
	Label   string // "" is the last spawn's recorded label; a first spawn needs one
	Goal    string // --goal, required
	Harness string // --harness: this spawn's harness, recorded on it; "" is the configured one
}

// Spawn starts a worker for req.Bead: it resolves the goal, opens its git_worktree
// workspace through herdr, records the spawn with its brief, launches the harness, and
// confirms the start. Nothing is recorded until the workspace exists; a launch that fails
// after the record leaves an unconfirmed spawn (exit 8) that bs end clears. An open spawn
// whose process died is resumed instead (resumed true).
func (c *Core) Spawn(ctx context.Context, req SpawnReq) (sp logbook.Spawn, resumed bool, err error) {
	g, own, err := c.parseGoal(req.Goal)
	if err != nil {
		return logbook.Spawn{}, false, exit(ExitRefused, "", err, "%s", req.Bead)
	}
	if req.Label != "" {
		if err := CheckLabel(req.Label); err != nil {
			return logbook.Spawn{}, false, exit(ExitRefused, "", err, "%s: --label", req.Bead)
		}
	}
	// Another project's bead would make a worker named for that project, which this
	// queen's mail can't reach (motif 3): its queen spawns it.
	if owner := BeadOwner(req.Bead); owner != c.Project {
		next := ""
		if owner != "" {
			next = bsname.Cmd + " send --project " + owner + " " + Queen(owner) + ` "..."`
		}
		return logbook.Spawn{}, false, exit(ExitRefused, next, nil, "%s is not a %s bead (want %s-<n>[.<m>…]); nothing was made", req.Bead, c.Project, c.Project)
	}
	bead, err := c.Beads.Show(ctx, req.Bead)
	if err != nil {
		if errors.Is(err, port.ErrNoBead) {
			return logbook.Spawn{}, false, exit(ExitRefused, "bd show "+req.Bead, err, "no bead %s", req.Bead)
		}
		return logbook.Spawn{}, false, exit(ExitCantCheck, "", err, "can't read bead %s", req.Bead)
	}
	name := Name(bead.ID)
	if err := herdr.CheckAgentName(name); err != nil {
		return logbook.Spawn{}, false, exit(ExitRefused, "", err, "%s can't be a worker's name; nothing was made", name)
	}
	if c.Kind == config.CommandKind && c.Command != nil {
		unlock, err := c.spawnLock(ctx, name)
		if err != nil {
			return logbook.Spawn{}, false, err
		}
		defer unlock()
	}
	var ended *logbook.Spawn // the last spawn, ended: its kept worktree and branch are reused
	switch prev, ok, err := c.Logbook.LatestSpawn(ctx, name); {
	case err != nil:
		return logbook.Spawn{}, false, err
	case ok && !prev.Ended:
		if req.Label != "" && req.Label != prev.Label {
			return logbook.Spawn{}, false, exit(ExitRefused, bsname.Cmd+" spawn --goal "+req.Goal+" "+bead.ID, nil,
				"%s is spawned and not ended, labelled %q, not %q; a resume keeps its label, so leave --label off", name, prev.Label, req.Label)
		}
		return prev, true, c.resume(ctx, prev, bead, req)
	case ok:
		ended = &prev
	}
	// A closed bead's work is done: a new spawn on it would start a goal nobody wants, and
	// status would read that over the completion it replaces (bees-13.78.4).
	if bead.Status == "closed" {
		return logbook.Spawn{}, false, exit(ExitRefused, "bd reopen "+bead.ID, nil, "%s is closed; nothing was made (reopen it to spawn on it again)", bead.ID)
	}
	// An ended worker comes back under its recorded label (bees-2.8); a first spawn names one.
	label := req.Label
	if label == "" && ended != nil {
		label = ended.Label
	}
	if label == "" {
		return logbook.Spawn{}, false, exit(ExitRefused, bsname.Cmd+` spawn --goal `+req.Goal+` --label "<words>" `+bead.ID, nil,
			"%s: a first spawn needs --label; nothing was made\n%s", name, LabelRules)
	}
	at, err := c.beadAt(ctx, bead)
	if err != nil {
		return logbook.Spawn{}, false, err
	}
	target, err := c.resolveReview(ctx, &g)
	if err != nil {
		return logbook.Spawn{}, false, err
	}
	ws, reused, err := c.workspace(ctx, name, target, ended)
	if err != nil {
		return logbook.Spawn{}, false, err
	}
	if err := c.refuse(g, ws); err != nil {
		return logbook.Spawn{}, false, err
	}
	var create []string
	if ws.Kind == config.CommandKind && !reused {
		create = c.Command.Create
	}
	if err := c.refuseCmds(ctx, g, name, bead.ID, ws, create); err != nil {
		return logbook.Spawn{}, false, err
	}
	deny, err := c.deny(ctx)
	if err != nil {
		return logbook.Spawn{}, false, err
	}
	if ws.Kind == config.CommandKind { // its guards come from git only in a git checkout: said (ADR-0010 Config)
		switch in, err := c.guardIn(ctx); {
		case err != nil: // deny just built them, so this is a probe that failed since
			c.warn("%s: a command workspace: can't tell whether its repo is a git checkout, so which guards apply: %v", name, err)
		case in.Git:
			c.warn("%s: a command workspace in a git checkout: its worker is denied pushes to the default branch and force pushes", name)
		default:
			c.warn("%s: a command workspace in a repo that isn't a git checkout: no git guard applies, only [guards] deny", name)
		}
	}
	kind := cmp.Or(req.Harness, c.HarnessName)
	h := c.harnessOf(kind)
	if h == nil {
		return logbook.Spawn{}, false, exit(ExitRefused, bsname.Cmd+" config", nil, "%s: this bs has no adapter for harness %s; nothing was made", name, kind)
	}
	text := prompt.Text(prompt.Worker, c.Project, h.Kind(), ws.Kind, g.Types(), own)
	if g.Reviewed() {
		host, err := c.host(prompt.Worker)
		if err != nil {
			return logbook.Spawn{}, false, exit(ExitRefused, bsname.Cmd+" config", err, "%s: its review host's section can't be read; nothing was made", name)
		}
		text += host
	}
	// Prepare is pure, so it runs before anything is made: a refused launch leaves nothing.
	launch, err := h.Prepare(c.workerStart(name, ws, doorbell(name, bead.ID, req.By), deny, text))
	if err != nil {
		return logbook.Spawn{}, false, exit(ExitRefused, "", err, "%s: its harness refused the launch; nothing was made", name)
	}
	launch.Files = withPrompt(launch.Files, text)
	// A launch too long to type would fail after the record (motif 8): check its longest
	// form now, before anything is made.
	worst, err := c.worstArgs(name, launch)
	if err != nil {
		return logbook.Spawn{}, false, exit(ExitRefused, bsname.Cmd+" config", err, "%s: its harness's start files can't be written; nothing was made", name)
	}
	if err := herdr.CheckAgentArgs(worst); err != nil {
		return logbook.Spawn{}, false, exit(ExitRefused, "", err, "%s: the launch can't be typed into a pane; nothing was made", name)
	}
	session := cmp.Or(req.Session, logbook.DefaultSession)
	var hw herdr.Workspace
	remade := false // a reused branch whose worktree was gone, checked out again here
	if ws.Kind == config.CommandKind {
		if hw, err = c.openCommand(ctx, session, &ws, name, bead.ID, label, reused); err != nil {
			return logbook.Spawn{}, false, err
		}
	} else if reused {
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
		if ws.Kind == config.CommandKind {
			err := c.herdr(session).CloseWorkspace(context.WithoutCancel(ctx), hw.ID)
			if !reused {
				err = errors.Join(err, c.remove(context.WithoutCancel(ctx), name, bead.ID, ws))
			}
			return err
		}
		if reused {
			err := c.herdr(session).CloseWorkspace(context.WithoutCancel(ctx), hw.ID)
			if remade {
				_, rerr := c.git(context.WithoutCancel(ctx), ws.Repo, "worktree", "remove", ws.Dir)
				err = errors.Join(err, rerr, untrust(h, ws.Dir))
			}
			return err
		}
		return errors.Join(c.unmake(context.WithoutCancel(ctx), session, hw.ID, ws), untrust(h, ws.Dir))
	}
	// A harness that starts only in a folder it trusts is told to trust this one, now that
	// it exists: only the path bs just made (bees-13.13.5).
	if err := trust(h, ws.Dir); err != nil {
		err = errors.Join(err, undo())
		return logbook.Spawn{}, false, exit(ExitFail, "", err, "%s: %s couldn't trust its worktree %s; nothing was recorded", name, kind, ws.Dir)
	}
	if err := c.track(ctx, ws, target); err != nil {
		err = errors.Join(err, undo())
		return logbook.Spawn{}, false, exit(ExitFail, "", err, "%s: couldn't set its branch's upstream to %s's head; nothing was recorded", name, target.c.URL)
	}
	// Where the branch starts: a pr goal's change must be ahead of it (pr_head).
	if ws.Branch != "" {
		tip, ok, err := c.vcs(ws).Branch(ctx, ws.Dir, ws.Branch)
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
		Name: name, By: req.By, Bead: bead.ID, Label: label, Harness: kind,
		Herdr: logbook.Pane{Session: session, Workspace: hw.ID, Pane: hw.Pane}, Workspace: ws,
		Goal: g, ConfigDir: c.ConfigDir, At: at, PruneIgnore: c.PruneIgnore,
	}
	sp.Record = launch.Record
	hint := "" // how the worker hands its work to the target
	switch {
	case target != nil && target.stacks:
		hint = c.Review.Create(where(ws))
	case target != nil:
		hint = c.Review.PushHint(ctx, ws.Dir, target.c)
	}
	if sp.ID, err = c.Logbook.RecordSpawn(ctx, req.By, sp, brief(bead, g, ws, target, hint, c.Guards.AllowMergeInto)); err != nil {
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
	if g, _, err := c.parseGoal(req.Goal); err == nil && g.String() != e.String() {
		return exit(ExitRefused, bsname.Cmd+" goal "+sp.Name+" "+req.Goal, nil,
			"%s is spawned and not ended, and it was spawned with --goal %s, not --goal %s; a resume keeps its goal", sp.Name, e.Goal, g)
	}
	// A resume restarts the recorded harness, never the configured one or another --harness:
	// only it can resume its conversation, or look for its process (ADR-0010's switch
	// refusal; motif 14).
	if req.Harness != "" && req.Harness != sp.Harness {
		return exit(ExitRefused, bsname.Cmd+" spawn "+goalFlags(e.Goal)+" "+bead.ID, nil,
			"%s is spawned and not ended, on harness %s, not %s; a resume keeps its harness, so leave --harness off", sp.Name, sp.Harness, req.Harness)
	}
	h := c.harnessOf(sp.Harness)
	if h == nil {
		return exit(ExitRefused, bsname.Cmd+" end --incomplete "+sp.Name+", then "+bsname.Cmd+" spawn "+goalFlags(e.Goal)+" "+bead.ID, nil,
			"%s was spawned on harness %s, which this bs has no adapter for; nothing started", sp.Name, sp.Harness)
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
	own, err := c.ownOf(e.Goal, sp.Name)
	if err != nil {
		return err
	}
	text := prompt.Text(prompt.Worker, c.Project, h.Kind(), sp.Workspace.Kind, e.Types(), own)
	if e.Reviewed() {
		host, err := c.host(prompt.Worker)
		if err != nil {
			return exit(ExitRefused, bsname.Cmd+" config", err, "%s: its review host's section can't be read; nothing started", sp.Name)
		}
		text += host
	}
	st0 := c.workerStart(sp.Name, sp.Workspace, doorbell(sp.Name, bead.ID, sp.By), deny, text)
	// It has a conversation once it read its inbox, or told bs which (agy's, observed by
	// its first bs call): the recorded facts and the observed ones, the observed newer.
	st0.Continue = sp.Read || len(sp.Observed) > 0
	st0.Record = maps.Clone(sp.Record)
	if st0.Record == nil && len(sp.Observed) > 0 {
		st0.Record = map[string]string{}
	}
	maps.Copy(st0.Record, sp.Observed)
	launch, err := h.Prepare(st0)
	if err != nil {
		return exit(ExitRefused, "", err, "%s: its harness refused the restart; nothing started", sp.Name)
	}
	launch.Files = withPrompt(launch.Files, text)
	if err := trust(h, sp.Workspace.Dir); err != nil {
		return exit(ExitFail, "", err, "%s: %s couldn't trust its worktree %s; nothing started", sp.Name, sp.Harness, sp.Workspace.Dir)
	}
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
// spawn's and a resume's alike: one place for its workspace, prompt, PATH, guards and
// shell timeout.
func (c *Core) workerStart(name string, ws logbook.Workspace, bell string, deny []string, text string) port.Start {
	return port.Start{Role: port.Worker, Name: name, Dir: ws.Dir, Branch: ws.Branch, PushTo: ws.Push, Repo: ws.Repo, PromptFile: "{{prompt.md}}", Prompt: text, Doorbell: bell,
		Path: c.WorkerPath, Deny: deny, ShellTimeout: ShellTimeout}
}

// refuse runs the spawn-time refusals of goal g that need no network, in workspace ws,
// for bs spawn and every bs goal amend.
func (c *Core) refuse(g goal.Goal, ws logbook.Workspace) error {
	if ws.Kind == config.CommandKind { // its branch is its VCS commands', never git's
		if on := g.OnGit(); len(on) > 0 && g.Type != "branch" {
			return exit(ExitRefused, "", nil, "--goal %s: %s read a git branch, which a command workspace has none of", g, strings.Join(on, ", "))
		}
		switch {
		case g.Type == "branch" || g.Type == "push" || g.Type == "pr" && g.Target != "":
			return exit(ExitRefused, "", nil, "--goal %s: a command workspace has no git branch to cut at a change's head or measure against a base; give it --goal pr or a [goal.<name>] of its own", g)
		case (g.Type == "pr" || len(g.OnBranch()) > 0) && ws.Branch == "":
			return exit(ExitRefused, bsname.Cmd+" config", nil, "--goal %s: a command workspace with no VCS has no branch to hand off: set [workspace.command] branch, pushed, has and descends (docs/SETUP.md)", g)
		}
	}
	if (g.Type == "branch" || g.Type == "pr" || g.Type == "push") && ws.Branch == "" {
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

// target is a pr= or push= goal's change as spawn found it: the worker's own branch is
// cut at its head (ADR-0010 Amendment 4).
type target struct {
	c      port.Change
	stacks bool // pr=<url>: the branch is measured against the target's head ref
}

// resolveReview runs the spawn-time refusals of a pr, push or review goal g (Rule G), for
// bs spawn and every bs goal amend: the project's checkout must belong to a repo on the
// review host, which g freezes; a target must be an open change of that repo, recorded in
// its one spelling. A pr= target must not be a fork's: a stacked branch is cut from the
// repo's own. A push target whose head lives elsewhere (a fork) must let the project push
// to it, and no open spawn may own it (its branch is the target's head): a fix to a
// bees-owned change is its owner's (Q3). A host that couldn't be asked (network, auth) is
// exit 6, never 2. It returns the target a pr= or push= workspace is cut at.
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
	case g.Type == "pr" && v.Foreign:
		return nil, exit(ExitRefused, "", nil, "--goal %s: its head is in a fork (%s), and a stacked branch is cut from this repo's own", g, v.ForeignRepo)
	case g.Type == "push" && !v.Editable:
		return nil, exit(ExitRefused, "ask its author to allow edits by maintainers", nil,
			"--goal %s: its head is in %s, which doesn't let this project push to it", g, v.ForeignRepo)
	}
	g.Target = r.URL
	switch g.Type {
	case "push":
		if err := c.unowned(ctx, g, v); err != nil {
			return nil, err
		}
	case "pr":
	default:
		return nil, nil
	}
	return &target{c: v, stacks: g.Type == "pr"}, nil
}

// unowned refuses push goal g on change v while an open spawn owns v: its recorded branch
// is v's head. Two workers never write one branch; the owner fixes its own change.
func (c *Core) unowned(ctx context.Context, g *goal.Goal, v port.Change) error {
	if v.Foreign {
		return nil // a fork's branch no spawn of this project's can be on
	}
	sps, err := c.Logbook.LatestSpawns(ctx)
	if err != nil {
		return err
	}
	for _, sp := range sps {
		if sp.Ended || sp.Workspace.Branch != v.Head {
			continue
		}
		e, err := c.Logbook.Goal(ctx, sp)
		if err != nil {
			return err
		}
		return exit(ExitRefused, bsname.Cmd+" spawn "+goalFlags(e.Goal)+" "+sp.Bead+" (it resumes "+sp.Name+"), or "+bsname.Cmd+" send "+sp.Name+" \"...\"", nil,
			"--goal %s: %s owns it (its branch %s is the change's head), and only it pushes there", g, sp.Name, v.Head)
	}
	return nil
}

// unmake takes back the worktree and branch a spawn made but couldn't record, or a second
// spawn would find them with nothing to prune them (motif 8). The branch is the spawn's
// own: workspace refused one that already existed.
func (c *Core) unmake(ctx context.Context, session, workspace string, ws logbook.Workspace) error {
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
// git checkout is git_worktree, always on a branch of its own; [workspace.command] makes it
// command (commandWorkspace); none arrives with bees-13.10. Its trunk is the branch the repo has checked out; it is
// measured against that, or against t's head ref when it stacks on t. When ended, name's
// last spawn, left its branch (its worktree kept, or gone with the branch kept), it is
// reused (reused true): the work there is the new spawn's to build on, never refused with
// no way on (motif 5).
func (c *Core) workspace(ctx context.Context, name string, t *target, ended *logbook.Spawn) (ws logbook.Workspace, reused bool, err error) {
	if c.Repo == "" {
		return logbook.Workspace{}, false, exit(ExitRefused, "", nil, "project %s has no repo: run it in the repo, or set paths.repo", c.Project)
	}
	if c.Kind == config.CommandKind {
		return c.commandWorkspace(name, ended)
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
	if c.Kind != "" && c.Kind != config.GitWorktree {
		return logbook.Workspace{}, false, exit(ExitRefused, bsname.Cmd+" config", nil, "workspace kind %s (%s) isn't built yet (none: bees-13.10)", c.Kind, c.KindWhy)
	}
	trunk, err := c.git(ctx, c.Repo, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return logbook.Workspace{}, false, refusedOrCant(err, "git -C "+c.Repo+" switch main", "%s has no branch checked out to cut from", c.Repo)
	}
	base := trunk
	if t != nil && t.stacks {
		base = t.c.Head
	}
	dir := filepath.Join(c.Worktrees, name)
	if ok, err := c.kept(ctx, ended, dir); err != nil {
		return logbook.Workspace{}, false, exit(ExitCantCheck, "", err, "can't tell whether %s is %s's kept worktree", dir, name)
	} else if ok {
		ws = ended.Workspace
		ws.Trunk, ws.Push = trunk, pushTo(t)
		if t != nil {
			ws.Base = base
		}
		return ws, true, nil
	}
	if _, err := os.Stat(dir); err == nil {
		return logbook.Workspace{}, false, exit(ExitRefused, bsname.Cmd+" prune", nil, "%s already exists", dir)
	}
	ws = logbook.Workspace{Kind: config.GitWorktree, Dir: dir, Repo: c.Repo, Branch: name, Base: base, Trunk: trunk, Push: pushTo(t)}
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

// pushTo is the head branch a push goal's worker pushes onto: t's, unless it stacks.
func pushTo(t *target) string {
	if t == nil || t.stacks {
		return ""
	}
	return t.c.Head
}

// writeFiles writes l.Files to the cache dir as <name>-<key>-* (0600) and l.Dir as one
// <name>-dir-* tree (0700 dirs, 0600 files), replacing each {{key}} and {{dir}} in l.Args
// with its path, after removing this session's earlier files.
func (c *Core) writeFiles(name string, l port.Launch) ([]string, error) {
	args := slices.Clone(l.Args)
	if len(l.Files) == 0 && len(l.Dir) == 0 {
		return args, nil
	}
	if err := checkDir(l.Dir); err != nil {
		return nil, err
	}
	if err := c.removeFiles(name); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.CacheDir, 0o700); err != nil {
		return nil, err
	}
	for _, key := range slices.Sorted(maps.Keys(l.Files)) {
		f, err := os.CreateTemp(c.CacheDir, filePattern(name, key))
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
		fill(args, key, f.Name())
	}
	if len(l.Dir) == 0 {
		return args, nil
	}
	dir, err := os.MkdirTemp(c.CacheDir, filePattern(name, "dir"))
	if err != nil {
		return nil, err
	}
	for _, rel := range slices.Sorted(maps.Keys(l.Dir)) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(l.Dir[rel]), 0o600); err != nil {
			return nil, err
		}
	}
	fill(args, "dir", dir)
	return args, nil
}

// checkDir refuses a Launch.Dir path that would land outside its directory.
func checkDir(d map[string]string) error {
	for rel := range d {
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("start dir path %q isn't a relative path inside it", rel)
		}
	}
	return nil
}

// worstArgs is l.Args as writeFiles would make them at their longest: each file's * as
// os.CreateTemp's longest, a uint32's 10 digits; an error for a Dir writeFiles refuses.
func (c *Core) worstArgs(name string, l port.Launch) ([]string, error) {
	args := slices.Clone(l.Args)
	for key := range l.Files {
		fill(args, key, filepath.Join(c.CacheDir, strings.Replace(filePattern(name, key), "*", "4294967295", 1)))
	}
	if len(l.Dir) > 0 {
		fill(args, "dir", filepath.Join(c.CacheDir, strings.Replace(filePattern(name, "dir"), "*", "4294967295", 1)))
	}
	return args, checkDir(l.Dir)
}

// filePattern is the os.CreateTemp pattern of name's start file key: <name>-<key>-*.
func filePattern(name, key string) string {
	ext := filepath.Ext(key)
	return name + "-" + strings.ReplaceAll(strings.TrimSuffix(key, ext), "_", "-") + "-*" + ext
}

// fill replaces each {{key}} in args with path.
func fill(args []string, key, path string) {
	for i := range args {
		args[i] = strings.ReplaceAll(args[i], "{{"+key+"}}", path)
	}
}

func brief(b port.Bead, g goal.Goal, ws logbook.Workspace, t *target, hint string, mergeInto []string) string {
	s := fmt.Sprintf("You own %s: %s\n\nYour goal: --goal %s. Hand it off with "+bsname.Cmd+" done when it holds.\n", b.ID, b.Title, g)
	if len(mergeInto) > 0 { // the guard can't see a PR's base: the brief says which (guards.allow_merge_into)
		s += fmt.Sprintf("The keeper lets you merge your own change when its base matches %s; check its base first, and never merge into the default branch.\n", strings.Join(mergeInto, " or "))
	}
	switch {
	case t != nil && t.stacks:
		s += fmt.Sprintf("Your branch %s starts at %s's head (%s) and stacks on it: open your change onto %s: %s\n", ws.Branch, g.Target, oid(t.c.HeadCommit), t.c.Head, hint)
	case t != nil:
		s += fmt.Sprintf("Your branch %s starts at %s's head (%s). Push your commits onto that change: %s\n", ws.Branch, g.Target, oid(t.c.HeadCommit), hint)
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

// makeWorkspace makes ws and opens a herdr workspace on it: herdr cuts the worker's branch
// and its worktree from the trunk, or from t's head commit once the review host has
// fetched it (herdr worktree create --base takes a commit: probed, herdr 0.9.5). ws.Dir
// becomes herdr's answer. Nothing is recorded yet: a failure leaves nothing behind.
func (c *Core) makeWorkspace(ctx context.Context, session string, ws *logbook.Workspace, t *target, label string) (herdr.Workspace, error) {
	name := filepath.Base(ws.Dir)
	from := ws.Base
	if t != nil {
		if err := c.Review.Fetch(ctx, ws.Repo, t.c); err != nil {
			return herdr.Workspace{}, exit(ExitCantCheck, "", err, "%s: can't fetch %s's head; nothing was made", name, t.c.URL)
		}
		from = t.c.HeadCommit
	}
	hw, err := c.herdr(session).CreateWorktree(ctx, ws.Repo, from, ws.Branch, ws.Dir, label)
	if err != nil {
		return hw, exit(ExitFail, "", err, "%s: herdr couldn't make its worktree; nothing was recorded", name)
	}
	ws.Dir = hw.Path // herdr makes it where asked; its answer is the record
	return hw, nil
}

// track sets a push goal's branch's upstream to its target's head ref, on the remote the
// trunk tracks (origin when it tracks none), so the worker's branch never reads as one of
// its own to publish (#119's review). A fork's target has no upstream here: the brief's
// push command names it.
func (c *Core) track(ctx context.Context, ws logbook.Workspace, t *target) error {
	if t == nil || t.stacks || t.c.Foreign {
		return nil
	}
	remote, err := c.git(ctx, ws.Repo, "for-each-ref", "--format=%(upstream:remotename)", "refs/heads/"+ws.Trunk)
	if err != nil {
		return err
	}
	remote = cmp.Or(remote, "origin")
	if _, err := c.git(ctx, ws.Repo, "config", "branch."+ws.Branch+".remote", remote); err != nil {
		return err
	}
	_, err = c.git(ctx, ws.Repo, "config", "branch."+ws.Branch+".merge", "refs/heads/"+t.c.Head)
	return err
}

// trust tells h to trust dir, when h starts only in a folder it trusts (port.Truster).
func trust(h port.Harness, dir string) error {
	if t, ok := h.(port.Truster); ok && dir != "" {
		return t.Trust(dir)
	}
	return nil
}

// untrust takes back trust's entry for dir.
func untrust(h port.Harness, dir string) error {
	if t, ok := h.(port.Truster); ok && dir != "" {
		return t.Untrust(dir)
	}
	return nil
}
