// Package bee starts and ends bees: one Claude Code session per bead, each in its
// own herdr workspace and git worktree (ADR-0006).
package bee

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/project"
	"github.com/jmmshn/bees/internal/prompt"
)

var (
	ErrNoAgent    = errors.New("no live agent with that name")
	ErrRunning    = errors.New("bee is already running")
	ErrOrphan     = errors.New("a herdr workspace is labelled with this bead but bs has no record of it")
	ErrWaiting    = errors.New("claude is waiting at a prompt") // started, but needs an answer in its pane
	ErrWrongRepo  = errors.New("bead does not belong to this project")
	ErrNotSpawned = errors.New("bs has no record of spawning this bee")
)

// Workspace is a herdr workspace opened on a git worktree.
type Workspace struct{ ID, Label, Pane, Path string }

// Agent is a live agent in a herdr pane.
type Agent struct{ Name, Pane string }

// Bead is the part of a bead bs reads.
type Bead struct{ ID, Title, Description string }

// Herdr is the slice of herdr bs drives.
type Herdr interface {
	CreateWorktree(ctx context.Context, repo, branch, path, label string) (Workspace, error)
	StartClaude(ctx context.Context, pane, name string, args ...string) error // ErrWaiting when blocked at a prompt
	RemoveWorktree(ctx context.Context, workspace string) error               // already gone → nil
	Agent(ctx context.Context, name string) (Agent, error)                    // ErrNoAgent when not live
	Workspaces(ctx context.Context) ([]Workspace, error)
}

// Beads reads beads.
type Beads interface {
	Show(ctx context.Context, id string) (Bead, error)
}

// Fleet spawns and ends the bees of one project.
type Fleet struct {
	P           project.Project
	LB          *logbook.Logbook
	H           Herdr
	B           Beads
	Me          string // the queen doing the spawning
	ForeignRepo bool   // skip the check that the bead belongs to P (--repo)
}

var herdrName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Name is the bee's one name for a bead: session, herdr agent, branch, worktree dir
// and address. Bead ids are Jira-style, <project>-<n>[.<m>…] (R27), and project names
// have no "-", so the name is the id with "." → "-": bees-12.3 → bees-12-3.
func Name(bead string) (string, error) {
	n := strings.ReplaceAll(bead, ".", "-")
	if !herdrName.MatchString(n) {
		return "", fmt.Errorf("%q can't be a bee name (lowercase, starting with a letter, at most 32 characters)", bead)
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

// Started describes a bee Spawn started.
type Started struct {
	Name, Workspace string
	Resumed         bool // restarted in its recorded workspace, continuing its conversation
	Waiting         bool // Claude is waiting at a prompt (e.g. folder trust) in its pane
}

// Spawn starts a bee for bead, or restarts it in its recorded workspace if its
// session has died.
func (f Fleet) Spawn(ctx context.Context, bead string) (Started, error) {
	name, err := Name(bead)
	if err != nil {
		return Started{}, err
	}
	sp, ok, err := f.LB.LatestSpawn(ctx, name)
	if err != nil {
		return Started{}, err
	}
	if ok && !sp.Ended {
		if _, err := f.H.Agent(ctx, name); err == nil {
			return Started{}, fmt.Errorf("%s: %w", name, ErrRunning)
		} else if !errors.Is(err, ErrNoAgent) {
			return Started{}, err
		}
		return f.start(ctx, sp, true)
	}
	if !f.ForeignRepo && !Belongs(f.P.Name, bead) {
		return Started{}, fmt.Errorf("%s is not a %s bead (want %s-<n>[.<m>]): %w (or pass --repo)", bead, f.P.Name, f.P.Name, ErrWrongRepo)
	}
	b, err := f.B.Show(ctx, bead)
	if err != nil {
		return Started{}, err
	}
	wss, err := f.H.Workspaces(ctx)
	if err != nil {
		return Started{}, err
	}
	if i := slices.IndexFunc(wss, func(w Workspace) bool { return w.Label == bead }); i >= 0 {
		return Started{}, fmt.Errorf("%s: %w; check it, then remove it with: herdr worktree remove --workspace %s --force", bead, ErrOrphan, wss[i].ID)
	}

	ws, err := f.H.CreateWorktree(ctx, f.P.Repo, name, f.P.Repo+"-WT/"+name, bead)
	if err != nil {
		return Started{}, err
	}
	// Record straight away: any failure after this point is healed by spawning again.
	sp = logbook.Spawn{Name: name, Bead: bead, Workspace: ws.ID, Pane: ws.Pane, Worktree: ws.Path, Branch: name, Repo: f.P.Repo}
	if sp.ID, err = f.LB.RecordSpawn(ctx, f.Me, sp, brief(b)); err != nil {
		return Started{}, err
	}
	return f.start(ctx, sp, false)
}

// beeSettings is passed with --settings: Claude reads settings.local.json from the main
// checkout even in a worktree, so per-bee settings can't live in a file. The bee's bs
// finds its name from its Claude process and its project from the worktree's git dir.
const beeSettings = `{"crossSessionInbound":"accept"}`

// start starts Claude with a doorbell as its first prompt, so the bee reads its unread
// messages without needing a ring. A resumed bee continues its last conversation in
// the worktree instead of starting its task over.
func (f Fleet) start(ctx context.Context, sp logbook.Spawn, resume bool) (Started, error) {
	st := Started{Name: sp.Name, Workspace: sp.Workspace, Resumed: resume}
	doorbell := fmt.Sprintf("[bs] you are %s, working on %s. Run: bs inbox   (reply with: bs send %s \"...\")", sp.Name, sp.Bead, f.Me)
	args := []string{"--name", sp.Name, "--settings", beeSettings}
	// In another repo (--repo, now or when first spawned) the bee's own bs resolves that
	// repo's project, so a system prompt naming P would mislead it.
	if !f.ForeignRepo && cmp.Or(sp.Repo, f.P.Repo) == f.P.Repo {
		args = append(args, "--append-system-prompt", prompt.System(f.P.Name))
	}
	args = append(args, doorbell)
	if resume {
		args = append([]string{"--continue"}, args...)
	}
	err := f.H.StartClaude(ctx, sp.Pane, sp.Name, args...)
	if errors.Is(err, ErrWaiting) {
		st.Waiting, err = true, nil
	}
	return st, err
}

// End removes the bee's recorded workspace and records the end. It never asks herdr
// which workspace to remove. Ending an ended bee is a no-op. The bee's branch is
// deleted if fully merged, so the bead can be spawned again; branchKept reports an
// unmerged branch left in place with its work.
func (f Fleet) End(ctx context.Context, name string) (branchKept bool, err error) {
	sp, ok, err := f.LB.LatestSpawn(ctx, name)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("%s: %w", name, ErrNotSpawned)
	}
	if sp.Ended {
		return false, nil
	}
	if err := f.H.RemoveWorktree(ctx, sp.Workspace); err != nil {
		return false, err
	}
	repo := cmp.Or(sp.Repo, f.P.Repo)
	if exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+sp.Branch).Run() == nil {
		branchKept = exec.CommandContext(ctx, "git", "-C", repo, "branch", "-d", sp.Branch).Run() != nil
	}
	return branchKept, f.LB.RecordEnd(ctx, f.Me, sp.ID)
}

func brief(b Bead) string {
	return fmt.Sprintf("You own %s: %s\n\n%s\n\nRun `bd show %s` for the full bead.", b.ID, b.Title, b.Description, b.ID)
}
