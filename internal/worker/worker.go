// Package worker starts and ends workers: one Claude Code session per bead, each in its
// own herdr workspace and git worktree (ADR-0006).
package worker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/project"
	"github.com/jmmshn/bees/internal/prompt"
)

var (
	ErrRunning = errors.New("worker is already running")
	ErrOrphan  = errors.New("a herdr workspace is open on this worker's worktree but bs has no record of it")
	ErrWaiting = errors.New("claude is waiting at a prompt") // started, but needs an answer in its pane
	// ErrUnconfirmed: herdr stopped waiting for Claude to be ready. Claude may be busy
	// with its first prompt or may have exited; only Claude's registry can tell.
	ErrUnconfirmed = errors.New("herdr could not confirm that claude started")
	ErrWrongRepo   = errors.New("bead does not belong to this project")
	ErrNotSpawned  = errors.New("bs has no record of spawning this worker")
	ErrNoLabel     = errors.New("a first spawn needs a label")
	ErrBadLabel    = errors.New("not a valid label")
	ErrLabelChange = errors.New("the worker is recorded with another label")
	ErrTwice       = errors.New("named twice in one spawn")
	ErrNotStarted  = errors.New("not started: another worker in this spawn was refused")
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

// Workspace is a herdr workspace opened on a git worktree.
type Workspace struct{ ID, Label, Pane, Path string }

// Bead is the part of a bead bs reads.
type Bead struct{ ID, Title, Description string }

// Herdr is the slice of herdr bs drives.
type Herdr interface {
	CreateWorktree(ctx context.Context, repo, branch, path, label string) (Workspace, error)
	// StartClaude runs claude with args in pane. ErrWaiting: blocked at a startup prompt.
	// ErrUnconfirmed: herdr gave up waiting. ErrUnsafeArg: an argument herdr can't pass.
	StartClaude(ctx context.Context, pane, name string, args ...string) error
	RemoveWorktree(ctx context.Context, workspace string) error // already gone → nil
	Workspaces(ctx context.Context) ([]Workspace, error)
}

// Sessions tells whether a Claude session is running, from Claude's own registry
// (ADR-0004), not from herdr: herdr leaves an agent unnamed when its start times out.
type Sessions interface {
	// Alive reports whether a session called name is live, waiting up to wait for one
	// to appear.
	Alive(ctx context.Context, name string, wait time.Duration) (bool, error)
}

// Beads reads beads.
type Beads interface {
	Show(ctx context.Context, id string) (Bead, error)
}

// Fleet spawns and ends the workers of one project.
type Fleet struct {
	P           project.Project
	LB          *logbook.Logbook
	H           Herdr
	S           Sessions
	B           Beads
	G           GitHub // nil: End proves a merge from git alone
	Me          string // the queen doing the spawning
	ForeignRepo bool   // skip the check that the bead belongs to P (--repo)
}

var herdrName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

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
	Resumed                bool // restarted in its recorded workspace
	Continued              bool // resumed with its last conversation (claude --continue)
	Waiting                bool // Claude is waiting at a prompt (e.g. folder trust) in its pane
}

// Spawn starts a worker for bead with label as its herdr workspace label, or restarts it
// in its recorded workspace if its session has died. A restart keeps the recorded
// label: label may be empty, and any other label is refused.
func (f Fleet) Spawn(ctx context.Context, bead, label string) (Started, error) {
	p, err := f.check(ctx, bead, label)
	if err != nil {
		return Started{}, err
	}
	sp, err := f.open(ctx, p)
	if err != nil {
		return Started{}, err
	}
	return f.start(ctx, sp, p.resume)
}

// Request asks SpawnAll for one worker.
type Request struct{ Bead, Label string }

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
	return errors.Is(err, ErrBadLabel) || errors.Is(err, ErrNoLabel) || errors.Is(err, ErrLabelChange) || errors.Is(err, ErrTwice)
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
			plans[i], out[i].Err = f.check(ctx, r.Bead, r.Label)
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
	sp                logbook.Spawn // resume: the recorded spawn
	resume            bool          // the worker is recorded and its session has died
	b                 Bead          // a first spawn's bead, for its brief
}

// check decides whether bead can be spawned, without touching herdr or the logbook.
func (f Fleet) check(ctx context.Context, bead, label string) (plan, error) {
	name, err := Name(bead)
	if err != nil {
		return plan{}, err
	}
	p := plan{name: name, bead: bead, label: label}
	if label != "" {
		if err := CheckLabel(label); err != nil {
			return p, err
		}
	}
	sp, ok, err := f.LB.LatestSpawn(ctx, name)
	if err != nil {
		return p, err
	}
	if ok && !sp.Ended {
		if label != "" && label != sp.Label {
			return p, fmt.Errorf("%s is labelled %q, not %q: %w; spawn it again without --label", name, sp.Label, label, ErrLabelChange)
		}
		// Fail closed: an unreadable registry must not start a second Claude.
		if alive, err := f.S.Alive(ctx, name, 0); err != nil {
			return p, err
		} else if alive {
			return p, fmt.Errorf("%s: %w", name, ErrRunning)
		}
		p.sp, p.resume = sp, true
		return p, nil
	}
	if label == "" {
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
	// Find an orphan by its worktree, never its label: the path is built forward from
	// the name, while a label is free text.
	wt := f.worktree(name)
	if i := slices.IndexFunc(wss, func(w Workspace) bool { return w.Path != "" && filepath.Clean(w.Path) == wt }); i >= 0 {
		return p, fmt.Errorf("%s: %w; check it, then remove it with: herdr worktree remove --workspace %s --force", name, ErrOrphan, wss[i].ID)
	}
	return p, nil
}

func (f Fleet) worktree(name string) string { return filepath.Join(f.P.Repo+"-WT", name) }

// open opens a first spawn's worktree and records the spawn; a resume has both already.
func (f Fleet) open(ctx context.Context, p plan) (logbook.Spawn, error) {
	if p.resume {
		return p.sp, nil
	}
	ws, err := f.H.CreateWorktree(ctx, f.P.Repo, p.name, f.worktree(p.name), p.label)
	if err != nil {
		return logbook.Spawn{}, err
	}
	// Record straight away: any failure after this point is healed by spawning again.
	sp := logbook.Spawn{Name: p.name, Bead: p.bead, Label: p.label, Workspace: ws.ID, Pane: ws.Pane, Worktree: ws.Path, Branch: p.name, Repo: f.P.Repo}
	sp.ID, err = f.LB.RecordSpawn(ctx, f.Me, sp, brief(p.b))
	return sp, err
}

// workerSettings is passed with --settings: Claude reads settings.local.json from the main
// checkout even in a worktree, so per-worker settings can't live in a file. The worker's bs
// finds its name from its Claude process and its project from the worktree's git dir.
const workerSettings = `{"crossSessionInbound":"accept"}`

// start starts Claude with a doorbell as its first prompt, so the worker reads its unread
// messages without needing a ring. A resumed worker continues its last conversation in
// the worktree instead of starting its task over, if it has one: a worker that never read
// its inbox since this spawn (its first start failed) starts fresh, since
// claude --continue exits when there is no conversation.
func (f Fleet) start(ctx context.Context, sp logbook.Spawn, resume bool) (Started, error) {
	// In another repo (--repo, now or when first spawned) the worker's own bs resolves that
	// repo's project: a system prompt naming P would mislead it, and its receipts go to
	// that project's logbook, so sp.Read can't tell and a restart always continues.
	foreign := f.ForeignRepo || cmp.Or(sp.Repo, f.P.Repo) != f.P.Repo
	st := Started{Name: sp.Name, Label: sp.Label, Workspace: sp.Workspace, Resumed: resume, Continued: resume && (sp.Read || foreign)}
	doorbell := fmt.Sprintf("[bs] you are %s, working on %s — run: bs inbox   (reply with: bs send %s \"...\")", sp.Name, sp.Bead, f.Me)
	args := []string{"--name", sp.Name, "--settings", workerSettings}
	if !foreign {
		file, err := prompt.File(prompt.Worker, f.P.Name)
		if err != nil {
			return st, err
		}
		args = append(args, "--append-system-prompt-file", file)
	}
	args = append(args, doorbell)
	if st.Continued {
		args = append([]string{"--continue"}, args...)
	}
	err := f.H.StartClaude(ctx, sp.Pane, sp.Name, args...)
	switch {
	case errors.Is(err, ErrWaiting):
		st.Waiting, err = true, nil
	case errors.Is(err, ErrUnconfirmed):
		// A worker goes straight to work on its doorbell, so herdr, which waits for an idle
		// prompt, usually times out on a healthy worker. Ask Claude's registry instead.
		alive, aerr := f.S.Alive(ctx, sp.Name, confirmWait)
		switch {
		case aerr != nil:
			err = aerr
		case alive:
			err = nil
		default:
			err = fmt.Errorf("%w: no Claude session named %s appeared within %s; read its pane (herdr pane read %s), then spawn again", err, sp.Name, confirmWait, sp.Pane)
		}
	}
	return st, err
}

// confirmWait is how long start waits for Claude's registry after herdr gives up.
// Claude registers about 3 s after launch (herdr 0.9.3, bees-1.14).
const confirmWait = 20 * time.Second

// End removes the worker's recorded workspace and records the end. It never asks herdr
// which workspace to remove. Ending an ended worker is a no-op. The worker's branch is always
// kept, with its work: Prune deletes it once merged.
func (f Fleet) End(ctx context.Context, name string) error {
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
	if err := f.H.RemoveWorktree(ctx, sp.Workspace); err != nil {
		return err
	}
	return f.LB.RecordEnd(ctx, f.Me, sp.ID)
}

// Pruned is what Prune found for one ended worker's branch.
type Pruned struct {
	Branch, Repo string
	Merge        Merge
	Why          string // how the work was proven merged, or why the branch was kept
	Deleted      bool
	DeleteErr    error // proven merged, but git branch -D failed
}

// Prune deletes the branch of each ended worker once its work is proven merged; dry only
// reports. It looks only at branches the logbook recorded (never guessed from git),
// skips branches already gone, and never touches a remote.
func (f Fleet) Prune(ctx context.Context, dry bool) ([]Pruned, error) {
	ended, err := f.LB.EndedSpawns(ctx)
	if err != nil {
		return nil, err
	}
	var out []Pruned
	for _, sp := range ended {
		repo := cmp.Or(sp.Repo, f.P.Repo)
		if gitRun(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+sp.Branch) != nil {
			continue
		}
		p := Pruned{Branch: sp.Branch, Repo: repo}
		p.Merge, p.Why = merged(ctx, f.G, repo, sp.Branch)
		if p.Merge == Merged && !dry {
			p.DeleteErr = gitRun(ctx, repo, "branch", "-D", sp.Branch)
			p.Deleted = p.DeleteErr == nil
		}
		out = append(out, p)
	}
	return out, nil
}

func brief(b Bead) string {
	return fmt.Sprintf("You own %s: %s\n\n%s\n\nRun `bd show %s` for the full bead.", b.ID, b.Title, b.Description, b.ID)
}
