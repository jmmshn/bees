// Package peon starts and ends peons: one Claude Code session per bead, each in its
// own herdr workspace and git worktree (ADR-0006).
package peon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
type Bead struct {
	ID, Title, Description, Parent string
	Labels                         []string
}

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
// and address. beads-995.8 → peon-995-8.
func Name(bead string) (string, error) {
	id, ok := strings.CutPrefix(bead, "beads-")
	n := "peon-" + strings.ReplaceAll(id, ".", "-")
	if !ok || id == "" || !herdrName.MatchString(n) {
		return "", fmt.Errorf("%q can't be a peon name (need beads-<id>, at most 27 characters after beads-)", bead)
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
	b, err := f.B.Show(ctx, bead)
	if err != nil {
		return "", err
	}
	if !f.ForeignRepo {
		if err := f.checkProject(ctx, b); err != nil {
			return "", err
		}
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
	sp = logbook.Spawn{Name: name, Bead: bead, Workspace: ws.ID, Pane: ws.Pane, Worktree: ws.Path, Branch: name}
	if sp.ID, err = f.LB.RecordSpawn(ctx, f.Me, sp); err != nil {
		return "", err
	}
	if _, err := f.LB.Send(ctx, f.Me, name, brief(b)); err != nil {
		return "", err
	}
	return name, f.start(ctx, sp)
}

// start writes the worktree's Claude settings and starts Claude with a doorbell as its
// first prompt, so the peon reads its unread messages without needing a ring.
func (f Fleet) start(ctx context.Context, sp logbook.Spawn) error {
	if err := writeSettings(sp.Worktree, map[string]string{"PNS_PROJECT": f.P.Name, "PNS_SESSION": sp.Name}); err != nil {
		return err
	}
	doorbell := fmt.Sprintf("[pns] you are %s, working on %s. Run: pns inbox   (reply with: pns send %s \"...\")", sp.Name, sp.Bead, f.Me)
	return f.H.StartClaude(ctx, sp.Pane, sp.Name, "--name", sp.Name, doorbell)
}

// checkProject requires the bead's repo label, or its nearest labelled ancestor's, to
// name this project.
func (f Fleet) checkProject(ctx context.Context, b Bead) error {
	for {
		if len(b.Labels) > 0 {
			if slices.Contains(b.Labels, f.P.Name) {
				return nil
			}
			return fmt.Errorf("%s is labelled %v, not %q: %w (pass --repo to override)", b.ID, b.Labels, f.P.Name, ErrWrongRepo)
		}
		if b.Parent == "" {
			return fmt.Errorf("%s and its ancestors have no repo label: %w (label the epic %q, or pass --repo)", b.ID, ErrWrongRepo, f.P.Name)
		}
		var err error
		if b, err = f.B.Show(ctx, b.Parent); err != nil {
			return err
		}
	}
}

// End removes the peon's recorded workspace and records the end. It never asks herdr
// which workspace to remove. Ending an ended peon is a no-op.
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

func brief(b Bead) string {
	return fmt.Sprintf("You own %s: %s\n\n%s\n\nRun `bd show %s` for the full bead.", b.ID, b.Title, b.Description, b.ID)
}

// writeSettings merges pns's keys into the worktree's .claude/settings.local.json.
func writeSettings(worktree string, env map[string]string) error {
	path := filepath.Join(worktree, ".claude", "settings.local.json")
	settings := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &settings); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	settings["crossSessionInbound"] = "accept"
	merged, _ := settings["env"].(map[string]any)
	if merged == nil {
		merged = map[string]any{}
	}
	for k, v := range env {
		merged[k] = v
	}
	settings["env"] = merged
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
