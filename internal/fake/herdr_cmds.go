package fake

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmmshn/bees/internal/herdr"
)

func (h *Herdr) workspaceCreate(s *server, _ string, f flags) ([]byte, error) {
	label, _ := f.get("--label")
	w := s.open(label, h.cwd(s, f))
	return result("workspace:create", map[string]any{"type": "workspace_created", "workspace": w.json(), "root_pane": w.panes[0].json(w)})
}

func (h *Herdr) workspaceList(s *server, _ string, _ flags) ([]byte, error) {
	ws := []any{}
	for _, w := range s.workspaces {
		ws = append(ws, w.json())
	}
	return result("workspace:list", map[string]any{"type": "workspace_list", "workspaces": ws})
}

func (h *Herdr) workspaceGet(s *server, _ string, f flags) ([]byte, error) {
	w := s.find(first(f.pos))
	if w == nil {
		return nil, refusal("workspace:get", "workspace_not_found", "workspace %s not found", first(f.pos))
	}
	return result("workspace:get", map[string]any{"type": "workspace_info", "workspace": w.json()})
}

// workspaceClose closes the workspace and every pane in it, ending their processes; the
// directory stays.
func (h *Herdr) workspaceClose(s *server, _ string, f flags) ([]byte, error) {
	w := s.find(first(f.pos))
	if w == nil {
		return nil, refusal("workspace:close", "workspace_not_found", "workspace %s not found", first(f.pos))
	}
	for _, p := range w.panes {
		p.hangUp()
	}
	w.panes = nil
	s.drop(w)
	return result("workspace:close", map[string]any{"type": "ok"})
}

// reportMetadata stores each --token, cutting its value to herdr.MaxToken without a word
// (bees-12.17). herdr 0.9.5 takes --source and --token in either order (probed, bees-13.2,
// against ADR-0010's note from an older herdr).
func (h *Herdr) reportMetadata(s *server, _ string, f flags) ([]byte, error) {
	if !f.has("--source") {
		return nil, usage("error: the following required arguments were not provided:\n  --source <ID>")
	}
	w := s.find(first(f.pos))
	if w == nil {
		return nil, refusal("workspace:report_metadata", "workspace_not_found", "workspace %s not found", first(f.pos))
	}
	for _, t := range f.opts["--token"] {
		k, v, ok := strings.Cut(t, "=")
		if !ok {
			return nil, usage("error: invalid value '%s' for '--token <NAME=VALUE>'", t)
		}
		w.tokens[k] = v[:min(len(v), herdr.MaxToken)]
	}
	return nil, nil // herdr prints nothing (probed)
}

// worktreeCreate makes the branch's worktree with real git and a workspace on it. Like
// herdr 0.9.5 it starts only from the repo's main checkout, never a linked worktree.
func (h *Herdr) worktreeCreate(s *server, _ string, f flags) ([]byte, error) {
	repo, _ := f.get("--cwd")
	branch, _ := f.get("--branch")
	path, _ := f.get("--path")
	label, _ := f.get("--label")
	if fi, err := os.Stat(filepath.Join(repo, ".git")); err != nil || !fi.IsDir() {
		return nil, refusal("worktree:create", "invalid_repository", "%s is not a repository's main checkout", repo)
	}
	args := []string{"-C", repo, "worktree", "add", "-b", branch, path}
	if base, ok := f.get("--base"); ok {
		args = append(args, base)
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return nil, refusal("worktree:create", "git_failed", "%s", strings.TrimSpace(string(out)))
	}
	w := s.open(label, path)
	w.worktree, w.repo = path, repo
	return result("worktree:create", map[string]any{"type": "worktree_created", "workspace": w.json(), "root_pane": w.panes[0].json(w),
		"worktree": map[string]string{"path": path, "branch": branch}})
}

// worktreeRemove removes a worktree workspace and its checkout. Without --force herdr
// refuses one with modified or untracked files (bees-7.4).
func (h *Herdr) worktreeRemove(s *server, _ string, f flags) ([]byte, error) {
	id, _ := f.get("--workspace")
	w := s.find(id)
	if w == nil {
		return nil, refusal("worktree:remove", "workspace_not_found", "workspace %s not found", id)
	}
	if !f.has("--force") {
		if out, _ := exec.Command("git", "-C", w.worktree, "status", "--porcelain").Output(); len(out) > 0 {
			return nil, refusal("worktree:remove", "dirty_worktree_requires_force", "worktree contains modified or untracked files, use --force")
		}
	}
	args := []string{"-C", w.repo, "worktree", "remove", w.worktree}
	if f.has("--force") {
		args = append(args, "--force")
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return nil, refusal("worktree:remove", "git_failed", "%s", strings.TrimSpace(string(out)))
	}
	for _, p := range w.panes {
		p.hangUp()
	}
	w.panes = nil
	s.drop(w)
	return result("worktree:remove", map[string]any{"type": "ok"})
}

func (h *Herdr) paneList(s *server, _ string, f flags) ([]byte, error) {
	panes := []any{}
	id, only := f.get("--workspace")
	if only && s.find(id) == nil {
		return nil, refusal("pane:list", "workspace_not_found", "workspace %s not found", id)
	}
	for _, w := range s.workspaces {
		if only && w.id != id {
			continue
		}
		for _, p := range w.panes {
			panes = append(panes, p.json(w))
		}
	}
	return result("pane:list", map[string]any{"type": "pane_list", "panes": panes})
}

// paneClose closes a pane, hanging up its processes; an emptied workspace closes.
func (h *Herdr) paneClose(s *server, _ string, f flags) ([]byte, error) {
	w, p := s.pane(first(f.pos))
	if p == nil {
		return nil, refusal("pane:close", "pane_not_found", "pane %s not found", first(f.pos))
	}
	p.hangUp()
	w.panes = slices.DeleteFunc(w.panes, func(x *pane) bool { return x == p })
	s.drop(w)
	return result("pane:close", map[string]any{"type": "ok"})
}

// paneMove moves a pane into another workspace (--new-tab --workspace W) or a new one
// (--new-workspace), as herdr 0.9.5 does (probed, bees-13.2): the pane gets a new id
// there, keeps its terminal and process, and the emptied workspace closes. The process's
// own HERDR_PANE_ID still names the old pane.
func (h *Herdr) paneMove(s *server, _ string, f flags) ([]byte, error) {
	from, p := s.pane(first(f.pos))
	if p == nil {
		return nil, refusal("pane:move", "pane_not_found", "pane %s not found", first(f.pos))
	}
	var to *workspace
	switch id, ok := f.get("--workspace"); {
	case f.has("--new-workspace"):
		label, _ := f.get("--label")
		s.next++
		to = &workspace{id: fmt.Sprintf("w%d", s.next), label: label, tokens: map[string]string{}}
		s.workspaces = append(s.workspaces, to)
	case f.has("--new-tab") && ok:
		if to = s.find(id); to == nil {
			return nil, refusal("pane:move", "workspace_not_found", "workspace %s not found", id)
		}
	default:
		return nil, usage("usage: herdr pane move <pane_id> --new-tab [--workspace ID] | --new-workspace")
	}
	prev := p.id
	from.panes = slices.DeleteFunc(from.panes, func(x *pane) bool { return x == p })
	moved := to.add(p.cwd)
	moved.agent = p.agent
	s.drop(from)
	return result("pane:move", map[string]any{"type": "pane_move", "move_result": map[string]any{
		"changed": true, "previous_pane_id": prev, "pane": moved.json(to)}})
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
