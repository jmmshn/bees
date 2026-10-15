package peon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// HerdrCLI drives the herdr command line. Every herdr command prints JSON.
type HerdrCLI struct{}

// herdrError is herdr's error envelope, printed with a non-zero exit.
type herdrError struct {
	Error struct{ Code, Message string } `json:"error"`
}

func herdr(ctx context.Context, out any, args ...string) (code string, err error) {
	b, err := exec.CommandContext(ctx, "herdr", args...).Output()
	if err != nil {
		var he herdrError
		var ee *exec.ExitError
		if errors.As(err, &ee) && json.Unmarshal(ee.Stderr, &he) == nil && he.Error.Code != "" {
			return he.Error.Code, fmt.Errorf("herdr %s: %s", args[0], he.Error.Message)
		}
		if json.Unmarshal(b, &he) == nil && he.Error.Code != "" {
			return he.Error.Code, fmt.Errorf("herdr %s: %s", args[0], he.Error.Message)
		}
		return "", fmt.Errorf("herdr %v: %w", args, err)
	}
	if out == nil {
		return "", nil
	}
	if err := json.Unmarshal(bytes.TrimSpace(b), out); err != nil {
		return "", fmt.Errorf("herdr %v: parse output: %w", args, err)
	}
	return "", nil
}

func (HerdrCLI) CreateWorktree(ctx context.Context, repo, branch, path, label string) (Workspace, error) {
	var r struct {
		Result struct {
			Workspace struct {
				ID string `json:"workspace_id"`
			} `json:"workspace"`
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
			Worktree struct {
				Path string `json:"path"`
			} `json:"worktree"`
		} `json:"result"`
	}
	if _, err := herdr(ctx, &r, "worktree", "create", "--cwd", repo, "--branch", branch, "--path", path, "--label", label, "--no-focus"); err != nil {
		return Workspace{}, err
	}
	if r.Result.Workspace.ID == "" || r.Result.RootPane.ID == "" {
		return Workspace{}, fmt.Errorf("herdr worktree create: no workspace or pane id in its output")
	}
	return Workspace{ID: r.Result.Workspace.ID, Label: label, Pane: r.Result.RootPane.ID, Path: r.Result.Worktree.Path}, nil
}

func (HerdrCLI) StartClaude(ctx context.Context, pane, name string, args ...string) error {
	code, err := herdr(ctx, nil, append([]string{"agent", "start", name, "--kind", "claude", "--pane", pane, "--"}, args...)...)
	if code == "agent_not_ready" { // Claude started but is blocked at a startup prompt
		return fmt.Errorf("%w: %w", err, ErrWaiting)
	}
	return err
}

func (HerdrCLI) RemoveWorktree(ctx context.Context, workspace string) error {
	code, err := herdr(ctx, nil, "worktree", "remove", "--workspace", workspace, "--force")
	if code == "workspace_not_found" {
		return nil
	}
	return err
}

func (HerdrCLI) Agent(ctx context.Context, name string) (Agent, error) {
	var r struct {
		Result struct {
			Agent struct {
				Name string `json:"name"`
				Pane string `json:"pane_id"`
			} `json:"agent"`
		} `json:"result"`
	}
	code, err := herdr(ctx, &r, "agent", "get", name)
	if code == "agent_not_found" {
		return Agent{}, ErrNoAgent
	}
	if err != nil {
		return Agent{}, err
	}
	return Agent{Name: r.Result.Agent.Name, Pane: r.Result.Agent.Pane}, nil
}

func (HerdrCLI) Workspaces(ctx context.Context) ([]Workspace, error) {
	var r struct {
		Result struct {
			Workspaces []struct {
				ID       string `json:"workspace_id"`
				Label    string `json:"label"`
				Worktree *struct {
					Path string `json:"checkout_path"`
				} `json:"worktree"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if _, err := herdr(ctx, &r, "workspace", "list"); err != nil {
		return nil, err
	}
	var out []Workspace
	for _, w := range r.Result.Workspaces {
		ws := Workspace{ID: w.ID, Label: w.Label}
		if w.Worktree != nil {
			ws.Path = w.Worktree.Path
		}
		out = append(out, ws)
	}
	return out, nil
}

// BeadsCLI reads beads with the bd command line.
type BeadsCLI struct{}

func (BeadsCLI) Show(ctx context.Context, id string) (Bead, error) {
	out, err := exec.CommandContext(ctx, "bd", "show", id, "--json").Output()
	if err != nil {
		return Bead{}, fmt.Errorf("bd show %s: %w", id, err)
	}
	var bs []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &bs); err != nil {
		return Bead{}, fmt.Errorf("bd show %s: %w", id, err)
	}
	if len(bs) == 0 {
		return Bead{}, fmt.Errorf("bd show %s: no such bead", id)
	}
	b := bs[0]
	return Bead{ID: b.ID, Title: b.Title, Description: b.Description}, nil
}

// IDs lists every bead id in the store, open or closed.
func (BeadsCLI) IDs(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "bd", "list", "--all", "--limit", "0", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("bd list: %w", err)
	}
	var bs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &bs); err != nil {
		return nil, fmt.Errorf("bd list: %w", err)
	}
	ids := make([]string, len(bs))
	for i, b := range bs {
		ids[i] = b.ID
	}
	return ids, nil
}

// CreateEpic creates an epic with an explicit id. --force because Jira-style ids don't
// use the store's own prefix.
func (BeadsCLI) CreateEpic(ctx context.Context, id, title string) error {
	out, err := exec.CommandContext(ctx, "bd", "create", "--silent", "--force", "--type", "epic", "--id", id, "--title", title).CombinedOutput()
	if err != nil {
		return fmt.Errorf("bd create %s: %w: %s", id, err, bytes.TrimSpace(out))
	}
	return nil
}
