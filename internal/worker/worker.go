// Package worker starts and ends workers: one Claude Code session per bead, each in its
// own herdr workspace and git worktree (ADR-0006).
package worker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/guards"
	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/project"
	"github.com/jmmshn/bees/internal/prompt"
)

var (
	ErrRunning = errors.New("worker is already running")
	ErrOrphan  = errors.New("a herdr workspace is open on this worker's worktree but bs has no record of it")
	ErrWaiting = errors.New("claude is waiting at a prompt") // started, but needs an answer in its pane
	// ErrPaneBusy: herdr refused the start, typing nothing, because something other than the
	// shell was in the pane's foreground.
	ErrPaneBusy = errors.New("the pane isn't at an idle shell prompt")
	// ErrUnconfirmed: herdr stopped waiting for the harness to be ready. It may be busy with
	// its first prompt or may have exited: only finding its process in the pane can tell.
	ErrUnconfirmed = errors.New("herdr could not confirm that the harness started")
	ErrWrongRepo   = errors.New("bead does not belong to this project")
	ErrNotSpawned  = errors.New("bs has no record of spawning this worker")
	ErrNoLabel     = errors.New("a first spawn needs a label")
	ErrBadLabel    = errors.New("not a valid label")
	ErrLabelChange = errors.New("the worker is recorded with another label")
	ErrBadPath     = harness.ErrBadPath
	ErrPathChange  = errors.New("the worker is recorded with another --path")
	ErrTwice       = errors.New("named twice in one spawn")
	ErrNotStarted  = errors.New("not started: another worker in this spawn was refused")
	ErrDirty       = errors.New("the worktree has modified or untracked files")
	ErrNotOurs     = errors.New("the recorded workspace is open on another worktree")
	// ErrHerdrDown: herdr didn't answer, so bs can't tell an open workspace from a gone one.
	ErrHerdrDown = errors.New("herdr is unreachable")
)

// LabelRules says what a worker's label must be. The queen writes the label; bs only checks it.
const LabelRules = `a label names the work for the herdr sidebar: 1-4 lowercase words of a-z and 0-9, single spaces, no punctuation, at most 32 characters, specific to the bead.
  good: "role prompts", "squash merge end", "natural workspace labels"
  bad:  "fix bug", "update docs", "Role-Prompts."`

var labelRule = regexp.MustCompile(`^[a-z0-9]+( [a-z0-9]+){0,3}$`)

// CheckLabel refuses a label that breaks LabelRules; it never rewrites one.
func CheckLabel(label string) error {
	if len(label) > 32 || !labelRule.MatchString(label) {
		return fmt.Errorf("%q: %w", label, ErrBadLabel)
	}
	return nil
}

// CheckPath refuses a --path dir that isn't an absolute path to an existing directory. The
// harness refuses, in Prepare, a character it can't render.
func CheckPath(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%q: %w: not absolute", dir, ErrBadPath)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%q: %w: not a directory", dir, ErrBadPath)
	}
	return nil
}

// Workspace is a herdr workspace: on a git worktree, or a plain one bs tagged with its
// worker and path (kind none).
type Workspace struct {
	ID, Label, Pane string
	Path            string // the worktree's checkout; "" for a plain workspace
	Worker          string // the worker token of a plain workspace bs opened; "" untagged
}

// Bead is the part of a bead bs reads.
type Bead struct{ ID, Title, Description string }

// Herdr is the slice of herdr bs drives.
type Herdr interface {
	// CreateWorktree cuts branch at path from base (a ref; "": repo's HEAD). repo must be a
	// main checkout: herdr 0.9.5 refuses a linked worktree as --cwd.
	CreateWorktree(ctx context.Context, repo, base, branch, path, label string) (Workspace, error)
	// StartAgent runs the harness of herdr kind kind with args in pane. ErrWaiting:
	// blocked at a startup prompt. ErrUnconfirmed: herdr gave up waiting. ErrUnsafeArg: an
	// argument herdr can't pass.
	StartAgent(ctx context.Context, pane, name, kind string, args ...string) error
	// RemoveWorktree removes workspace and its worktree; already gone → nil. Without force
	// it refuses a worktree with modified or untracked files: ErrDirty. Both: ErrHerdrDown
	// when herdr itself didn't answer.
	RemoveWorktree(ctx context.Context, workspace string, force bool) error
	Workspaces(ctx context.Context) ([]Workspace, error)
	// CreateWorkspace opens a plain workspace on dir. herdr 0.9.5 opens $HOME, exiting 0,
	// for a dir that doesn't exist: check it first.
	CreateWorkspace(ctx context.Context, dir, label string) (Workspace, error)
	// TagWorkspace tags workspace with its worker and path, since herdr lists no directory
	// for a plain workspace.
	TagWorkspace(ctx context.Context, workspace, worker, path string) error
	// CloseWorkspace closes a plain workspace, leaving its directory; already gone → nil.
	// ErrHerdrDown when herdr didn't answer.
	CloseWorkspace(ctx context.Context, workspace string) error
	// ProcessInfo is herdr's process-info for pane: the only thing bs asks herdr about a
	// running session, and only at start. ErrPaneGone: no such pane. ErrHerdrDown.
	ProcessInfo(ctx context.Context, pane string) (PaneProcesses, error)
	// RenameAgent names pane's agent in herdr: the sidebar's name, and a fence against a
	// twin's launch (agent_name_taken). bs never reads it back.
	RenameAgent(ctx context.Context, pane, name string) error
}

// Beads reads beads.
type Beads interface {
	Show(ctx context.Context, id string) (Bead, error)
}

// Fleet spawns and ends the workers of one project.
type Fleet struct {
	P             project.Project
	LB            *logbook.Logbook
	H             Herdr
	Procs         Procs
	B             Beads
	G             GitHub          // nil: End proves a merge from git alone
	Me            string          // the queen doing the spawning
	ForeignRepo   bool            // skip the check that the bead belongs to P (--repo)
	Harness       harness.Harness // every start's, built from the worker role's config
	HarnessName   string          // its config name, recorded in each spawned entry
	Delivery      config.Delivery // how work lands; a zero Kind is github_pr
	Guards        config.Guards   // [guards]: what every worker is denied or granted
	WorkspaceKind string          // the workspace kind of a first spawn: GitWorktree ("" too) or NoWorkspace
	DefaultPath   []string        // worker.path: a new or ended worker's Path when its Request has none
}

// kind is f's delivery kind; a Fleet built without config is github_pr, the default.
func (f Fleet) kind() string { return cmp.Or(f.Delivery.Kind, "github_pr") }

// gh is GitHub when the delivery kind uses it: manual never asks GitHub anything.
func (f Fleet) gh() GitHub {
	if f.kind() != "github_pr" {
		return nil
	}
	return f.G
}

// Workspace kinds (ADR-0009). A spawn recorded with no kind is a git worktree.
const (
	GitWorktree = "git_worktree"
	NoWorkspace = "none"
)

var herdrName = project.SessionName

// Name is the worker's one name for a bead: session, herdr agent, branch, worktree dir
// and address. Bead ids are Jira-style, <project>-<n>[.<m>…] (R27), and project names
// have no "-", so the name is the id with "." → "-": bees-12.3 → bees-12-3.
func Name(bead string) (string, error) {
	n := strings.ReplaceAll(bead, ".", "-")
	if !herdrName.MatchString(n) {
		return "", fmt.Errorf("%q can't be a worker name (lowercase, starting with a letter, at most 32 characters)", bead)
	}
	return n, nil
}

var beadNumber = regexp.MustCompile(`^-\d+(\.\d+)*$`)

// Belongs reports whether bead is one of project's: <project>-<n>[.<m>…].
func Belongs(project, bead string) bool {
	rest, ok := strings.CutPrefix(bead, project)
	return ok && beadNumber.MatchString(rest)
}

var workerNumber = regexp.MustCompile(`^(-\d+)+$`)

// IsWorkerOf reports whether name is the worker name of one of project's beads:
// <project>-<n>[-<m>…]. A bare prefix test would also take a session named bees-review.
func IsWorkerOf(project, name string) bool {
	rest, ok := strings.CutPrefix(name, project)
	return ok && workerNumber.MatchString(rest)
}

// Address maps to, a worker's name or a bead id of project, forward to the worker's name:
// bs end and send take either spelling (bees-6.1). A bead id whose worker was never spawned
// is refused, so nothing queues for a session that can't exist.
func Address(ctx context.Context, lb *logbook.Logbook, project, to string) (string, error) {
	if !Belongs(project, to) || !strings.Contains(to, ".") {
		return to, nil
	}
	name, err := Name(to)
	if err != nil {
		return "", err
	}
	if _, ok, err := lb.LatestSpawn(ctx, name); err != nil || ok {
		return name, err
	}
	return "", fmt.Errorf("%s is a bead id, whose worker is %s: %w", to, name, ErrNotSpawned)
}

// NextEpic returns the next free top-level id for project among ids: <project>-<max+1>.
func NextEpic(project string, ids []string) string {
	top := 0
	for _, id := range ids {
		rest, ok := strings.CutPrefix(id, project+"-")
		rest, _, _ = strings.Cut(rest, ".") // a child proves its epic's number is taken
		if n, err := strconv.Atoi(rest); ok && err == nil && n > top {
			top = n
		}
	}
	return fmt.Sprintf("%s-%d", project, top+1)
}

// Started describes a worker Spawn started.
type Started struct {
	Name, Label, Workspace string
	Resumed                bool  // restarted in its recorded workspace
	Continued              bool  // resumed with its last conversation (claude --continue)
	Waiting                bool  // Claude is waiting at a prompt (e.g. folder trust) in its pane
	Warn                   error // started, but naming it in herdr failed
}

// Spawn starts a worker for bead with label as its herdr workspace label, or restarts it
// in its recorded workspace if its session has died. A restart keeps the recorded
// label: label may be empty, and any other label is refused. Respawning an ended
// worker with an empty label reuses its recorded one.
func (f Fleet) Spawn(ctx context.Context, bead, label string) (Started, error) {
	p, err := f.check(ctx, Request{Bead: bead, Label: label})
	if err != nil {
		return Started{}, err
	}
	sp, err := f.open(ctx, p)
	if err != nil {
		return Started{}, err
	}
	return f.start(ctx, sp, p.resume)
}

// Request asks SpawnAll for one worker. Path is put first on the PATH of the worker's
// shell commands (bs spawn --path), so a live test's workers run the build under test
// (bees-8.19). Like the label, a nil Path reuses the recorded one, on a restart or after
// an end; a new worker, or an ended one recorded without one, takes Fleet.DefaultPath. A
// restart refuses another.
type Request struct {
	Bead, Label string
	Path        []string
}

// Result is what SpawnAll did for one Request.
type Result struct {
	Bead    string
	Started Started
	Err     error
	Took    time.Duration
}

// Refused reports whether err is a mistake in the spawn request itself (a bad,
// missing or changed label, a worker named twice): fix the command and run it again.
func Refused(err error) bool {
	return errors.Is(err, ErrBadLabel) || errors.Is(err, ErrNoLabel) || errors.Is(err, ErrLabelChange) || errors.Is(err, ErrTwice) ||
		errors.Is(err, ErrBadPath) || errors.Is(err, ErrPathChange)
}

// SpawnAll spawns a worker for each request, as Spawn does, and returns one Result per
// request in order. It checks every request before touching anything: if any is
// Refused, none starts, and the others fail with ErrNotStarted. Then each worker's
// worktree and spawn record are made one worker at a time (git and herdr state stay
// simple), and the slow part, waiting for each Claude to start, runs concurrently.
// Past the checks, one failure doesn't stop the others. A worker named twice is refused
// the second time: two starts of one worker would race into two Claudes.
func (f Fleet) SpawnAll(ctx context.Context, reqs []Request) []Result {
	out := make([]Result, len(reqs))
	plans := make([]plan, len(reqs))
	seen := map[string]bool{}
	refused := false
	for i, r := range reqs {
		t0 := time.Now()
		out[i].Bead = r.Bead
		if name, err := Name(r.Bead); err == nil && seen[name] {
			out[i].Err = fmt.Errorf("%s: %w", name, ErrTwice)
		} else {
			plans[i], out[i].Err = f.check(ctx, r)
			seen[plans[i].name] = out[i].Err == nil // a refused first try doesn't block a corrected repeat
		}
		out[i].Took = time.Since(t0)
		refused = refused || Refused(out[i].Err)
	}
	if refused {
		for i := range out {
			if out[i].Err == nil {
				out[i].Err = ErrNotStarted
			}
		}
		return out
	}
	var wg sync.WaitGroup
	for i, p := range plans {
		if out[i].Err != nil {
			continue
		}
		t0 := time.Now()
		sp, err := f.open(ctx, p)
		if err != nil {
			out[i].Err, out[i].Took = err, out[i].Took+time.Since(t0)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i].Started, out[i].Err = f.start(ctx, sp, p.resume)
			out[i].Took += time.Since(t0)
		}()
	}
	wg.Wait()
	return out
}

// plan is a checked spawn; nothing has been touched yet.
type plan struct {
	name, bead, label string
	path              []string          // a first spawn's --path
	sp                logbook.Spawn     // resume: the recorded spawn
	resume            bool              // the worker is recorded and its session has died
	b                 Bead              // a first spawn's bead, for its brief
	record            map[string]string // the harness's Launch.Record, for a first spawn's entry
}

// check decides whether bead can be spawned, without touching herdr or the logbook.
func (f Fleet) check(ctx context.Context, req Request) (plan, error) {
	bead, label := req.Bead, req.Label
	name, err := Name(bead)
	if err != nil {
		return plan{}, err
	}
	p := plan{name: name, bead: bead, label: label, path: req.Path}
	sp, ok, err := f.LB.LatestSpawn(ctx, name)
	if err != nil {
		return p, err
	}
	if p.path == nil && ok {
		p.path = sp.Path
	}
	if p.path == nil && (!ok || sp.Ended) { // a live worker keeps what it was recorded with
		p.path = f.DefaultPath
	}
	// The recorded dirs too: a build removed since would quietly start the shared bs.
	for _, d := range p.path {
		if err := CheckPath(d); err != nil {
			return p, fmt.Errorf("%s: %w", name, err)
		}
	}
	// Check the launch line before anything is made, so a launch too long to type leaves
	// no worktree or record behind. Prepare is pure, and a file's * is at most 10 digits.
	dir, err := prompt.Dir()
	if err != nil {
		return p, err
	}
	// An unreadable delivery fragment or a bad guard refuses the spawn here too; start
	// checks both again at launch, against the spawn's recorded repo.
	if _, err := Delivery(f.Delivery, f.Guards); err != nil {
		return p, fmt.Errorf("%s: %w", name, err)
	}
	if err := CheckWorkspace(f.WorkspaceKind, f.Delivery); err != nil {
		return p, fmt.Errorf("%s: %w", name, err)
	}
	sp0 := logbook.Spawn{Name: name, Bead: bead, Worktree: f.worktree(name), Branch: name, Repo: f.P.Repo, Path: p.path}
	if f.WorkspaceKind == NoWorkspace {
		// Workers share this directory, so only the keeper names it: a repo the chain
		// found by git is the checkout bs runs in, the queen's own (bees-12.28).
		if !f.P.RepoNamed() {
			return p, fmt.Errorf("%s: workspace kind none opens workers on paths.repo, which is unset; set it in the project's config file to the directory they share (bs config shows the repo it found: %s, from %s)", name, f.P.Repo, f.P.RepoFrom)
		}
		sp0 = logbook.Spawn{Name: name, Bead: bead, Kind: NoWorkspace, Dir: f.P.Repo, Repo: f.P.Repo, Path: p.path}
		if fi, err := os.Stat(f.P.Repo); err != nil || !fi.IsDir() { // herdr would open $HOME without a word
			return p, fmt.Errorf("%s: workspace directory %s is not a directory", name, f.P.Repo)
		}
	}
	protected := len(f.Guards.AllowMergeInto) > 0 && f.protected(ctx, sp0) // only a merge grant needs GitHub's word before anything is made
	r, err := f.rules(ctx, sp0, protected)
	if err != nil {
		return p, fmt.Errorf("%s: %w", name, err)
	}
	st := f.startOf(sp0, filepath.Join(dir, f.P.Name+"-worker.md"), true, r)
	l, err := f.Harness.Prepare(st)
	if errors.Is(err, harness.ErrNoResume) { // no resume ever starts, so a fresh start's line is the longest
		st.Continue = false
		l, err = f.Harness.Prepare(st)
	}
	if err != nil {
		return p, err
	}
	p.record = l.Record
	args, err := fill(l, func(key string) (string, error) {
		return filepath.Join(dir, strings.Replace(filePattern(name, key), "*", "4294967295", 1)), nil
	})
	if err != nil {
		return p, err
	}
	if err := CheckAgentArgs(args); err != nil {
		return p, fmt.Errorf("%s: %w", name, err)
	}
	if label != "" {
		if err := CheckLabel(label); err != nil {
			return p, err
		}
	}
	if ok && !sp.Ended {
		if label != "" && label != sp.Label {
			return p, fmt.Errorf("%s is labelled %q, not %q: %w; spawn it again without --label", name, sp.Label, label, ErrLabelChange)
		}
		if req.Path != nil && !slices.Equal(req.Path, sp.Path) {
			return p, fmt.Errorf("%s was spawned with --path %q, not %q: %w; spawn it again without --path", name, sp.Path, req.Path, ErrPathChange)
		}
		// Fail closed: a process that can't be looked up must not get a twin (motif 7).
		if alive, _, _, err := SpawnAlive(ctx, f.LB, f.Procs, sp); err != nil {
			return p, err
		} else if alive {
			return p, fmt.Errorf("%s: %w", name, ErrRunning)
		}
		p.sp, p.resume = sp, true
		return p, nil
	}
	if label == "" && ok {
		p.label = sp.Label // an ended worker comes back under its recorded label
	}
	if p.label == "" {
		return p, fmt.Errorf("%s: %w", name, ErrNoLabel)
	}
	if !f.ForeignRepo && !Belongs(f.P.Name, bead) {
		return p, fmt.Errorf("%s is not a %s bead (want %s-<n>[.<m>]): %w (or pass --repo)", bead, f.P.Name, f.P.Name, ErrWrongRepo)
	}
	if p.b, err = f.B.Show(ctx, bead); err != nil {
		return p, err
	}
	wss, err := f.H.Workspaces(ctx)
	if err != nil {
		return p, err
	}
	if f.WorkspaceKind == NoWorkspace {
		// Workers may share paths.repo, so find an orphan by the worker token bs tags a
		// plain workspace with.
		if i := slices.IndexFunc(wss, func(w Workspace) bool { return w.Worker == name }); i >= 0 {
			return p, fmt.Errorf("%s: %w; check it, then close it with: herdr workspace close %s", name, ErrOrphan, wss[i].ID)
		}
		return p, nil
	}
	// Find an orphan by its worktree, never its label: the path is built forward from
	// the name, while a label is free text. The latest spawn's recorded worktree counts
	// too, since paths.worktrees may have moved since it ended.
	wts := []string{f.worktree(name)}
	if ok && sp.Worktree != "" {
		wts = append(wts, filepath.Clean(sp.Worktree))
	}
	if i := slices.IndexFunc(wss, func(w Workspace) bool { return w.Path != "" && slices.Contains(wts, filepath.Clean(w.Path)) }); i >= 0 {
		return p, fmt.Errorf("%s: %w; check it, then remove it with: herdr worktree remove --workspace %s --force", name, ErrOrphan, wss[i].ID)
	}
	return p, nil
}

// worktree is where worker name's worktree goes: <paths.worktrees>/<name>.
func (f Fleet) worktree(name string) string { return filepath.Join(f.P.Worktrees, name) }

// open opens a first spawn's workspace and records the spawn; a resume has both already.
func (f Fleet) open(ctx context.Context, p plan) (logbook.Spawn, error) {
	if p.resume {
		return p.sp, nil
	}
	if f.WorkspaceKind == NoWorkspace {
		return f.openPlain(ctx, p)
	}
	base := ""
	if f.P.Base != "" { // a linked worktree: herdr cuts from its HEAD, run from the main checkout
		out, err := gitOut(ctx, f.P.Base, "rev-parse", "HEAD")
		if err != nil {
			return logbook.Spawn{}, err
		}
		base = strings.TrimSpace(out)
	}
	ws, err := f.H.CreateWorktree(ctx, f.P.Repo, base, p.name, f.worktree(p.name), f.herdrLabel(p.label))
	if err != nil {
		return logbook.Spawn{}, err
	}
	// Record straight away: any failure after this point is healed by spawning again.
	sp := logbook.Spawn{Name: p.name, Bead: p.bead, Label: p.label, Workspace: ws.ID, Pane: ws.Pane, Worktree: ws.Path, Branch: p.name, Repo: f.P.Repo, Base: f.P.BaseRepo(), Harness: cmp.Or(f.HarnessName, harness.Default), Record: p.record, Path: p.path}
	sp.ID, err = f.LB.RecordSpawn(ctx, f.Me, sp, brief(p.b))
	return sp, err
}

// openPlain opens a plain herdr workspace (kind none) on paths.repo, tags it with its
// worker, and records the spawn. A failure before the record closes the workspace, so
// spawning again starts clean (motif 8).
func (f Fleet) openPlain(ctx context.Context, p plan) (logbook.Spawn, error) {
	dir := f.P.Repo // check found it a directory
	ws, err := f.H.CreateWorkspace(ctx, dir, f.herdrLabel(p.label))
	if err != nil {
		return logbook.Spawn{}, err
	}
	sp := logbook.Spawn{Name: p.name, Bead: p.bead, Label: p.label, Workspace: ws.ID, Pane: ws.Pane, Kind: NoWorkspace, Dir: dir, Repo: f.P.Repo, Base: f.P.BaseRepo(), Harness: cmp.Or(f.HarnessName, harness.Default), Record: p.record, Path: p.path}
	if err = f.H.TagWorkspace(ctx, ws.ID, p.name, dir); err == nil {
		sp.ID, err = f.LB.RecordSpawn(ctx, f.Me, sp, brief(p.b))
	}
	if err != nil {
		if cerr := f.H.CloseWorkspace(ctx, ws.ID); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing workspace %s again: %w", ws.ID, cerr))
		}
		return logbook.Spawn{}, err
	}
	return sp, nil
}

// herdrLabel is the workspace label herdr shows for a worker labelled label: the project,
// then the queen's words. herdr's sidebar shows a plain workspace with no repo above it,
// so every kind carries the project (ADR-0009, bees-12.14). One forward mapping: bs never
// parses a herdr label back (motif 3), and the logbook records only the words.
func (f Fleet) herdrLabel(label string) string { return f.P.Name + " · " + label }

// rules builds sp's guards; protected is GitHub's word on the default branch (f.protected).
func (f Fleet) rules(ctx context.Context, sp logbook.Spawn, protected bool) (guards.Rules, error) {
	repo := cmp.Or(sp.Repo, f.P.Repo)
	if sp.Kind == NoWorkspace {
		return Rules(ctx, repo, config.Delivery{Kind: f.kind()}, f.Guards, guards.Worker{Repo: sp.Repo, Dir: sp.Dir, Protected: protected})
	}
	return Rules(ctx, repo, config.Delivery{Kind: f.kind()}, f.Guards, guards.Worker{Branch: cmp.Or(sp.Branch, sp.Name), Worktree: sp.Worktree, Repo: sp.Repo, Protected: protected})
}

// Rules builds the guards of worker w in repo, against the repo's real default branch
// (bees-11.7): w.DefaultBranches is filled from origin/HEAD and from the branch repo, the
// main checkout, has checked out, since origin/HEAD is unset in a repo that was never cloned.
func Rules(ctx context.Context, repo string, d config.Delivery, g config.Guards, w guards.Worker) (guards.Rules, error) {
	var heads []string
	if w.Dir != "" {
		// Kind none on a jj or hg checkout: no git default branch to read, so the guards
		// cover main and master. Any other git failure refuses (motif 7).
		err := gitRun(ctx, repo, "rev-parse", "--git-dir")
		if err != nil && !strings.Contains(err.Error(), "not a git repository") {
			return guards.Rules{}, fmt.Errorf("finding the default branch to guard: %w", err)
		}
		if err != nil {
			w.DefaultBranches = guards.DefaultBranches()
			return guards.Build(d, g, w)
		}
	}
	for _, ref := range []string{"refs/remotes/origin/HEAD", "HEAD"} {
		h, err := symRef(ctx, repo, ref)
		if err != nil {
			return guards.Rules{}, fmt.Errorf("finding the default branch to guard: %w", err)
		}
		heads = append(heads, strings.TrimPrefix(h, "origin/"))
	}
	w.DefaultBranches = guards.DefaultBranches(heads...)
	return guards.Build(d, g, w)
}

// CheckWorkspace refuses workspace kind with delivery d when the pair can't work: github_pr
// opens a PR from the worker's own branch, and a kind none worker has none, so its prompt
// would tell it to make one in the shared directory (bees-12.29). A fragment is the user's
// own wording, so it passes.
func CheckWorkspace(kind string, d config.Delivery) error {
	if kind == NoWorkspace && d.Fragment == "" && cmp.Or(d.Kind, "github_pr") == "github_pr" {
		return errors.New(`workspace.kind = "none" gives a worker no branch for delivery.kind = "github_pr" (the default) to open a PR from; set delivery.kind = "manual", or delivery.fragment to your own wording`)
	}
	return nil
}

// Delivery is the delivery fragment a role prompt gets under d (its fragment file, read
// now, else the kind's own), with g's merge grant if any.
func Delivery(d config.Delivery, g config.Guards) (string, error) {
	text, err := prompt.Delivery(cmp.Or(d.Kind, "github_pr"), config.Expand(d.Fragment))
	if err != nil {
		return "", err
	}
	return text + guards.Note(d, g), nil
}

// start starts the worker's harness with a doorbell as its first prompt, so the worker
// reads its unread messages without needing a ring. A resumed worker continues its last
// conversation in the worktree instead of starting its task over, if it has one: a worker
// that never read its inbox since this spawn (its first start failed) starts fresh, since
// claude --continue exits when there is no conversation.
func (f Fleet) start(ctx context.Context, sp logbook.Spawn, resume bool) (Started, error) {
	st := Started{Name: sp.Name, Label: sp.Label, Workspace: sp.Workspace, Resumed: resume, Continued: resume && sp.Read}
	// Read the delivery fragment before anything is made: an unreadable one stops the start.
	delivery, err := Delivery(f.Delivery, f.Guards)
	if err != nil {
		return st, err
	}
	r, err := f.rules(ctx, sp, f.protected(ctx, sp))
	if err != nil {
		return st, err
	}
	file, err := prompt.File(prompt.Worker, f.P.Name, sp.Kind, delivery)
	if err != nil {
		return st, err
	}
	// No env is pinned: the worker's bs finds its project, name and logbook from its herdr
	// pane, recorded in the spawned entry (ADR-0009 amendment 1).
	l, err := f.Harness.Prepare(f.startOf(sp, file, st.Continued, r))
	if err != nil {
		return st, err
	}
	// Only a worker that isn't running gets here, so earlier starts' files are spent.
	if err := removeFiles(sp.Name); err != nil {
		return st, err
	}
	var made []string
	args, err := fill(l, func(key string) (string, error) {
		path, err := prompt.Private(filePattern(sp.Name, key), l.Files[key])
		if err == nil {
			made = append(made, path)
		}
		return path, err
	})
	if err == nil {
		err = f.launch(ctx, sp, &st, args)
	}
	if err != nil {
		for _, path := range made {
			os.Remove(path)
		}
	}
	return st, err
}

// launch starts the harness in sp's pane and confirms the start: the harness process shows
// in the pane within confirmWait (ADR-0007, amended by ADR-0009). herdr's own wait is for
// an idle prompt, which a worker busy with its doorbell rarely reaches in time, so its
// timeout alone confirms nothing. A confirmed start records the process and its baseline
// descendants in a started entry, then names the agent in herdr.
//
// A harness already in the pane refuses the start: bs didn't record it, so it may be an
// earlier start of this worker that was never confirmed, or a spawn from before bs
// recorded starts; starting another beside it would make a twin (motif 7).
func (f Fleet) launch(ctx context.Context, sp logbook.Spawn, st *Started, args []string) error {
	switch p, ok, err := harnessIn(ctx, f.H, f.Procs, sp.Pane, f.Harness.Kind()); {
	case err != nil:
		return fmt.Errorf("%s: can't tell whether pane %s already runs %s: %w", sp.Name, sp.Pane, f.Harness.Kind(), err)
	case ok:
		return fmt.Errorf("%s: %w: pane %s already runs %s (pid %d), which bs has no start of; read it (herdr pane read %s): end it there, or bs end %s and spawn again", sp.Name, ErrRunning, sp.Pane, f.Harness.Kind(), p.PID, sp.Pane, sp.Name)
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	kick := make(chan struct{})
	seen := watch(wctx, f.H, f.Procs, sp.Pane, f.Harness.Kind(), confirmWait, kick)
	err := f.startAgent(ctx, sp.Pane, sp.Name, args)
	close(kick) // herdr is done waiting: look now, not a poll later
	switch {
	case errors.Is(err, ErrWaiting):
		st.Waiting = true
	case err != nil && !errors.Is(err, ErrUnconfirmed):
		cancel()
		<-seen // the watch is done before launch returns: no goroutine outlives the start
		return err
	}
	got := <-seen
	switch {
	case got.err != nil:
		return fmt.Errorf("%s: can't tell whether it started: %w", sp.Name, got.err)
	case got.p.PID == 0:
		return fmt.Errorf("%w: no %s process appeared in pane %s within %s; read it (herdr pane read %s), then spawn again", ErrUnconfirmed, f.Harness.Kind(), sp.Pane, confirmWait, sp.Pane)
	}
	rec := logbook.Start{Spawn: sp.ID, Name: sp.Name, Process: got.p, Harness: sp.Harness, Exe: got.exe, Baseline: got.baseline}
	if got.baselineErr != nil {
		rec.BaselineErr = got.baselineErr.Error()
	}
	if _, err := f.LB.RecordStart(ctx, rec); err != nil {
		return err
	}
	st.Warn = f.H.RenameAgent(ctx, sp.Pane, sp.Name)
	return nil
}

// startOf is the harness.Start of worker sp, with a doorbell as its first prompt so it
// reads its unread messages without needing a ring.
func (f Fleet) startOf(sp logbook.Spawn, promptFile string, cont bool, r guards.Rules) harness.Start {
	return harness.Start{
		Role: prompt.Worker, Name: sp.Name, Dir: cmp.Or(sp.Worktree, sp.Dir), PromptFile: promptFile, Continue: cont, Path: sp.Path,
		Doorbell: fmt.Sprintf("[bs] you are %s, working on %s — run: bs inbox   (reply with: bs send %s \"...\")", sp.Name, sp.Bead, f.Me),
		Guards:   r,
	}
}

// filePattern names worker name's files for Launch.Files key, for prompt.Private:
// settings.json → <name>-settings-*.json.
func filePattern(name, key string) string {
	stem, ext, _ := strings.Cut(key, ".")
	if ext != "" {
		ext = "." + ext
	}
	return name + "-" + stem + "-*" + ext
}

// removeFiles removes every per-start file of worker name: <name>-<stem>-*, where a stem
// starts with a letter, so this never takes the files of a worker whose name extends name
// (bees-12 and bees-12-3), nor a prompt file (<project>-worker.md). It also takes the
// fixed name of 8.3's first cut, <name>-settings.json.
func removeFiles(name string) error {
	return errors.Join(prompt.Remove(name+"-[a-z]*-*"), prompt.Remove(name+"-settings.json"))
}

// fill returns l.Args with each {{key}} of l.Files replaced by path(key). It refuses a
// key that breaks harness.FileKey, so removeFiles stays a forward glob.
func fill(l harness.Launch, path func(key string) (string, error)) ([]string, error) {
	args := slices.Clone(l.Args)
	keys := slices.Sorted(maps.Keys(l.Files))
	for _, key := range keys {
		if !harness.FileKey.MatchString(key) {
			return nil, fmt.Errorf("harness file key %q: want lowercase, starting with a letter", key)
		}
		p, err := path(key)
		if err != nil {
			return nil, err
		}
		for i := range args {
			args[i] = strings.ReplaceAll(args[i], "{{"+key+"}}", p)
		}
	}
	return args, nil
}

// protected reports whether GitHub protects the default branch of sp's repo. Without
// GitHub (or under manual delivery), or when gh fails, it says no: the worker is then
// told nothing about merges.
func (f Fleet) protected(ctx context.Context, sp logbook.Spawn) bool {
	gh := f.gh()
	if gh == nil {
		return false
	}
	ok, err := gh.Protected(ctx, cmp.Or(sp.Repo, f.P.Repo))
	return err == nil && ok
}

// confirmWait is how long a start waits for the harness process to show in its pane,
// counted from the launch, herdr's own wait included.
// Claude's shows within about 3 s of launch (herdr 0.9.3, bees-1.14).
var confirmWait = 20 * time.Second

// startAgent is StartAgent, tried again while herdr finds the pane busy. A new pane's
// shell runs its startup and prompt helpers (starship, mise) in the foreground for a
// moment, and herdr refuses a start then without typing anything (agent_pane_busy, herdr
// 0.9.5), so trying again can't start a twin (bees-12.33). Bounded by busyWait, well
// inside confirmWait, which the start's watch is already counting (motif 15).
func (f Fleet) startAgent(ctx context.Context, pane, name string, args []string) error {
	deadline := time.Now().Add(busyWait)
	for {
		err := f.H.StartAgent(ctx, pane, name, f.Harness.Kind(), args...)
		if !errors.Is(err, ErrPaneBusy) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(watchEvery):
		}
	}
}

var busyWait = 8 * time.Second

// EndOpts says how far ending a worker may go.
type EndOpts struct {
	Force bool // remove a worktree with uncommitted or untracked files, discarding them; end a moved worker
	// Forget records the end of a worker whose recorded workspace herdr has handed to
	// another worktree, leaving that workspace open: without it ErrNotOurs is a dead end.
	Forget bool
}

// End removes the worker's recorded workspace and records the end. It never asks herdr
// which workspace to remove, and removes the recorded one only while it is still the
// worker's (open on the recorded worktree, or for kind none tagged with the worker), so a
// reused workspace id can't take another worktree with it (bees-7.4). A worktree with
// uncommitted or untracked files is kept unless o.Force: ErrDirty. Under kind none, End
// only closes the workspace: the directory is the keeper's.
// Ending an ended worker is a no-op. The worker's branch is always kept, with its
// committed work: Prune deletes it once merged.
func (f Fleet) End(ctx context.Context, name string, o EndOpts) error {
	sp, ok, err := f.LB.LatestSpawn(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: %w", name, ErrNotSpawned)
	}
	if sp.Ended {
		return nil
	}
	e := f.end(ctx, sp, o)
	return cmp.Or(e.Err, e.Warn)
}

// Ending is what EndAll did with one worker.
type Ending struct {
	Name, Workspace string
	Branch          string // the worker's branch, kept; "" under kind none
	Gone            bool   // herdr no longer had the workspace, so only the end was recorded
	Forgot          bool   // the workspace is no longer the worker's: the end was recorded, the workspace left open
	Err             error  // the worker was kept: still live in the logbook
	Warn            error  // the worker was ended, but removing its per-start files failed
}

// EndAll ends every worker the logbook records as spawned and not yet ended, and nothing
// else: never a workspace guessed from herdr (motif 12). That includes a worker spawned with
// --repo for another project's bead (dotfiles-3): this project's queen recorded it. A
// worker it can't end stays live in the logbook, so running it again picks up where it
// stopped.
func (f Fleet) EndAll(ctx context.Context, o EndOpts) ([]Ending, error) {
	live, err := f.LB.LiveSpawns(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Ending, len(live))
	for i, sp := range live {
		out[i] = f.end(ctx, sp, o)
	}
	return out, nil
}

// end removes live spawn sp's workspace and records the end.
func (f Fleet) end(ctx context.Context, sp logbook.Spawn, o EndOpts) Ending {
	e := Ending{Name: sp.Name, Workspace: sp.Workspace, Branch: sp.Branch}
	if sp.Kind != "" && sp.Kind != GitWorktree && sp.Kind != NoWorkspace { // a newer bs's: never guess, even to --forget
		e.Err = fmt.Errorf("%s: recorded with workspace kind %q, which this bs doesn't know; nothing removed: end it with the bs that spawned it", sp.Name, sp.Kind)
		return e
	}
	wss, err := f.H.Workspaces(ctx)
	if err != nil {
		e.Err = fmt.Errorf("%s: can't tell whether workspace %s is open; nothing removed: %w", sp.Name, sp.Workspace, err)
		return e
	}
	i := slices.IndexFunc(wss, func(w Workspace) bool { return w.ID == sp.Workspace })
	e.Gone = i < 0
	plain := sp.Kind == NoWorkspace
	switch {
	case e.Gone && !o.Force:
		// Its pane may have moved to another workspace (a new pane id): ending it then
		// would record ended over a running worker (motif 12).
		alive, _, st, err := SpawnAlive(ctx, f.LB, f.Procs, sp)
		switch {
		case err != nil:
			e.Err = fmt.Errorf("%s: workspace %s is gone, and bs can't tell whether the worker still runs; nothing recorded (--force ends it anyway): %w", sp.Name, sp.Workspace, err)
			return e
		case alive:
			e.Err = fmt.Errorf("%s: workspace %s is gone, but the worker still runs (pid %d), so its pane moved: %w; close it where it runs now, or end it with --force", sp.Name, sp.Workspace, st.PID, ErrMoved)
			return e
		}
	case e.Gone:
	case plain && wss[i].Worker != sp.Name:
		if !o.Forget {
			e.Err = fmt.Errorf("%s: workspace %s is tagged %q, not as this worker's: %w; nothing closed (--forget records the end and leaves it open)", sp.Name, sp.Workspace, wss[i].Worker, ErrNotOurs)
			return e
		}
		e.Forgot = true
	case plain:
		if err := f.H.CloseWorkspace(ctx, sp.Workspace); err != nil {
			e.Err = fmt.Errorf("%s: %w", sp.Name, err)
			return e
		}
	case sp.Worktree == "" || filepath.Clean(wss[i].Path) != filepath.Clean(sp.Worktree):
		if !o.Forget {
			e.Err = fmt.Errorf("%s: workspace %s is open on %q, not its recorded worktree %q: %w; nothing removed (--forget records the end and leaves it open)", sp.Name, sp.Workspace, wss[i].Path, sp.Worktree, ErrNotOurs)
			return e
		}
		e.Forgot = true
	default:
		if err := f.H.RemoveWorktree(ctx, sp.Workspace, o.Force); err != nil {
			if errors.Is(err, ErrDirty) {
				err = fmt.Errorf("%w in %s; commit or move them, or end it with --force to discard them", ErrDirty, sp.Worktree)
			}
			e.Err = fmt.Errorf("%s: %w", sp.Name, err)
			return e
		}
	}
	if err := f.LB.RecordEnd(ctx, f.Me, sp.ID); err != nil {
		e.Err = err
		return e
	}
	e.Warn = removeFiles(sp.Name)
	return e
}

// Pruned is what Prune found for one ended worker's branch.
type Pruned struct {
	Branch, Repo string
	Merge        Merge
	Why          string // how the work was proven merged, or why the branch was kept
	InUse        string // the worktree that has the branch in use; kept, whatever Merge says
	Prunable     bool   // InUse's directory is gone: git worktree prune frees the branch
	Deleted      bool
	DeleteErr    error // proven merged, but git branch -D failed
}

// Prune deletes the branch of each ended worker once its work is proven merged; dry only
// reports. It looks only at branches the logbook recorded (never guessed from git),
// skips branches already gone, keeps any branch a worktree has checked out (someone may
// have taken it over: bees-8.4), and never touches a remote.
func (f Fleet) Prune(ctx context.Context, dry bool) ([]Pruned, error) {
	ended, err := f.LB.EndedSpawns(ctx)
	if err != nil {
		return nil, err
	}
	var out []Pruned
	for _, sp := range ended {
		repo := cmp.Or(sp.Repo, f.P.Repo)
		if sp.Branch == "" || gitRun(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+sp.Branch) != nil { // kind none has no branch
			continue
		}
		p := Pruned{Branch: sp.Branch, Repo: repo}
		p.Merge, p.Why = merged(ctx, f.gh(), repo, sp.Branch)
		if p.InUse, p.Prunable, err = checkedOut(ctx, repo, sp.Branch); err != nil {
			p.Merge, p.Why = Unknown, err.Error()
		}
		if p.Merge == Merged && p.InUse == "" && !dry {
			p.DeleteErr = gitRun(ctx, repo, "branch", "-D", sp.Branch)
			p.Deleted = p.DeleteErr == nil
			// git knows in-use states bs doesn't look for; its refusal names the worktree.
			if _, at, ok := strings.Cut(fmt.Sprint(p.DeleteErr), "used by worktree at "); ok {
				p.InUse, p.DeleteErr = strings.Trim(strings.TrimSpace(at), "'"), nil
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func brief(b Bead) string {
	return fmt.Sprintf("You own %s: %s\n\n%s\n\nRun `bd show %s` for the full bead.", b.ID, b.Title, b.Description, b.ID)
}
