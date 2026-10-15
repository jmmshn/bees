// Package peon starts and ends peons: one Claude Code session per bead, each in its
// own herdr workspace and git worktree (ADR-0006).
package peon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"

	"github.com/jmmshn/peons/internal/logbook"
	"github.com/jmmshn/peons/internal/project"
)

var (
	ErrNoAgent    = errors.New("no live agent with that name")
	ErrRunning    = errors.New("peon is already running")
	ErrOrphan     = errors.New("a herdr workspace is labelled with this bead but pns has no record of it; remove it by hand")
	ErrWrongRepo  = errors.New("bead does not belong to this project")
	ErrNotSpawned = errors.New("pns has no record of spawning this peon")
)

// Workspace is a herdr workspace opened on a git worktree.
type Workspace struct{ ID, Label, Pane, Path string }

// Agent is a live agent in a herdr pane.
type Agent struct{ Name, Pane string }

// Bead is the part of a bead pns reads.
type Bead struct{ ID, Title, Description string }

// Herdr is the slice of herdr pns drives.
type Herdr interface {
	CreateWorktree(ctx context.Context, repo, branch, path, label string) (Workspace, error)
	StartClaude(ctx context.Context, pane, name string, args ...string) error
	RemoveWorktree(ctx context.Context, workspace string) error // already gone → nil
	Agent(ctx context.Context, name string) (Agent, error)      // ErrNoAgent when not live
	Workspaces(ctx context.Context) ([]Workspace, error)
}

// Beads reads beads.
type Beads interface {
	Show(ctx context.Context, id string) (Bead, error)
}

// Fleet spawns and ends the peons of one project.
type Fleet struct {
	P           project.Project
	LB          *logbook.Logbook
	H           Herdr
	B           Beads
	Me          string // the chief doing the spawning
	ForeignRepo bool   // skip the check that the bead belongs to P (--repo)
}

var herdrName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Name is the peon's one name for a bead: session, herdr agent, branch, worktree dir
// and address. Beads live under a project epic named for the repo (R27), so the name is
// the bead id with "." → "-": peons.8 → peons-8.
func Name(bead string) (string, error) {
	n := strings.ReplaceAll(bead, ".", "-")
	if !herdrName.MatchString(n) {
		return "", fmt.Errorf("%q can't be a peon name (lowercase, starting with a letter, at most 32 characters)", bead)
	}
	return n, nil
}

// Spawn starts a peon for bead, or restarts it in its recorded workspace if its
// session has died. It returns the peon's name.
func (f Fleet) Spawn(ctx context.Context, bead string) (string, error) {
	name, err := Name(bead)
	if err != nil {
		return "", err
	}
	sp, ok, err := f.LB.LatestSpawn(ctx, name)
	if err != nil {
		return "", err
	}
	if ok && !sp.Ended {
		if _, err := f.H.Agent(ctx, name); err == nil {
			return name, fmt.Errorf("%s: %w", name, ErrRunning)
		} else if !errors.Is(err, ErrNoAgent) {
			return name, err
		}
		return name, f.start(ctx, sp) // resume
	}
	if !f.ForeignRepo && !strings.HasPrefix(bead, f.P.Name+".") {
		return "", fmt.Errorf("%s is not under the %q epic: %w (file it with bd create --parent %s, or pass --repo)", bead, f.P.Name, ErrWrongRepo, f.P.Name)
	}
	b, err := f.B.Show(ctx, bead)
	if err != nil {
		return "", err
	}
	wss, err := f.H.Workspaces(ctx)
	if err != nil {
		return "", err
	}
	if i := slices.IndexFunc(wss, func(w Workspace) bool { return w.Label == bead }); i >= 0 {
		return "", fmt.Errorf("%s (workspace %s): %w", bead, wss[i].ID, ErrOrphan)
	}

	ws, err := f.H.CreateWorktree(ctx, f.P.Repo, name, f.P.Repo+"-WT/"+name, bead)
	if err != nil {
		return "", err
	}
	// Record straight away: any failure after this point is healed by spawning again.
	sp = logbook.Spawn{Name: name, Bead: bead, Workspace: ws.ID, Pane: ws.Pane, Worktree: ws.Path, Branch: name, Repo: f.P.Repo}
	if sp.ID, err = f.LB.RecordSpawn(ctx, f.Me, sp, brief(b)); err != nil {
		return "", err
	}
	return name, f.start(ctx, sp)
}

// peonSettings is passed with --settings: Claude reads settings.local.json from the main
// checkout even in a worktree, so per-peon settings can't live in a file. The peon's pns
// finds its name from its Claude process and its project from the worktree's git dir.
const peonSettings = `{"crossSessionInbound":"accept"}`

// start starts Claude with a doorbell as its first prompt, so the peon reads its unread
// messages without needing a ring.
func (f Fleet) start(ctx context.Context, sp logbook.Spawn) error {
	doorbell := fmt.Sprintf("[pns] you are %s, working on %s. Run: pns inbox   (reply with: pns send %s \"...\")", sp.Name, sp.Bead, f.Me)
	return f.H.StartClaude(ctx, sp.Pane, sp.Name, "--name", sp.Name, "--settings", peonSettings, doorbell)
}

// End removes the peon's recorded workspace and records the end. It never asks herdr
// which workspace to remove. Ending an ended peon is a no-op. The peon's branch is
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
