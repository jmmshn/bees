package peon_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jmmshn/peons/internal/logbook"
	"github.com/jmmshn/peons/internal/peon"
	"github.com/jmmshn/peons/internal/project"
)

// fakeHerdr keeps workspaces and live agents in memory, and records removals.
type fakeHerdr struct {
	next       int
	workspaces []peon.Workspace
	agents     map[string]string // name → pane
	started    [][]string        // pane, name, args...
	removed    []string
	failStart  error
}

func (h *fakeHerdr) CreateWorktree(ctx context.Context, repo, branch, path, label string) (peon.Workspace, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return peon.Workspace{}, err
	}
	h.next++
	ws := peon.Workspace{ID: fmt.Sprintf("w%d", h.next), Label: label, Pane: fmt.Sprintf("w%d:p1", h.next), Path: path}
	h.workspaces = append(h.workspaces, ws)
	return ws, nil
}

func (h *fakeHerdr) StartClaude(ctx context.Context, pane, name string, args ...string) error {
	if h.failStart != nil {
		return h.failStart
	}
	h.started = append(h.started, append([]string{pane, name}, args...))
	h.agents[name] = pane
	return nil
}

func (h *fakeHerdr) RemoveWorktree(ctx context.Context, workspace string) error {
	h.removed = append(h.removed, workspace)
	h.workspaces = slices.DeleteFunc(h.workspaces, func(w peon.Workspace) bool { return w.ID == workspace })
	return nil
}

func (h *fakeHerdr) Agent(ctx context.Context, name string) (peon.Agent, error) {
	pane, ok := h.agents[name]
	if !ok {
		return peon.Agent{}, peon.ErrNoAgent
	}
	return peon.Agent{Name: name, Pane: pane}, nil
}

func (h *fakeHerdr) Workspaces(ctx context.Context) ([]peon.Workspace, error) {
	return h.workspaces, nil
}

// exit simulates the peon's Claude process ending: herdr clears its name.
func (h *fakeHerdr) exit(name string) { delete(h.agents, name) }

type fakeBeads map[string]peon.Bead

func (b fakeBeads) Show(ctx context.Context, id string) (peon.Bead, error) {
	bead, ok := b[id]
	if !ok {
		return peon.Bead{}, fmt.Errorf("no bead %s", id)
	}
	return bead, nil
}

type rig struct {
	fleet peon.Fleet
	h     *fakeHerdr
	lb    *logbook.Logbook
	repo  string
}

func newRig(t *testing.T) rig {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	lb, err := logbook.Open(ctx, filepath.Join(dir, "peons.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lb.Close() })
	h := &fakeHerdr{agents: map[string]string{}}
	beads := fakeBeads{
		"beads-995":   {ID: "beads-995", Title: "peons: design spec", Labels: []string{"peons"}},
		"beads-995.8": {ID: "beads-995.8", Title: "spawn and end peons", Description: "Build pns spawn.", Parent: "beads-995"},
		"beads-7qx":   {ID: "beads-7qx", Title: "dotfiles thing", Labels: []string{"dotfiles"}},
		"beads-nol":   {ID: "beads-nol", Title: "unlabelled"},
	}
	repo := filepath.Join(dir, "peons")
	p := project.Project{Name: "peons", Repo: repo, Logbook: filepath.Join(dir, "peons.db")}
	return rig{fleet: peon.Fleet{P: p, LB: lb, H: h, B: beads, Me: "chief-peons"}, h: h, lb: lb, repo: repo}
}

func TestName(t *testing.T) {
	for bead, want := range map[string]string{"beads-995.8": "peon-995-8", "beads-2db.15.3": "peon-2db-15-3", "beads-mgn": "peon-mgn"} {
		if got, err := peon.Name(bead); err != nil || got != want {
			t.Errorf("Name(%q) = %q, %v; want %q", bead, got, err, want)
		}
	}
	for _, bad := range []string{"beads-" + strings.Repeat("1.", 14) + "1", "beads-", "BEADS-X"} {
		if _, err := peon.Name(bad); err == nil {
			t.Errorf("Name(%q) succeeded, want error", bad)
		}
	}
}

func TestSpawnStartsAPeonInItsOwnWorkspace(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	name, err := r.fleet.Spawn(ctx, "beads-995.8")
	if err != nil {
		t.Fatal(err)
	}
	if name != "peon-995-8" {
		t.Fatalf("name = %q", name)
	}
	wt := r.repo + "-WT/peon-995-8"
	ws := r.h.workspaces[0]
	if ws.Path != wt || ws.Label != "beads-995.8" {
		t.Fatalf("workspace = %+v", ws)
	}
	var settings struct {
		CrossSessionInbound string            `json:"crossSessionInbound"`
		Env                 map[string]string `json:"env"`
	}
	b, err := os.ReadFile(filepath.Join(wt, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.CrossSessionInbound != "accept" || settings.Env["PNS_PROJECT"] != "peons" || settings.Env["PNS_SESSION"] != "peon-995-8" {
		t.Fatalf("settings = %s", b)
	}
	sp, ok, _ := r.lb.LatestSpawn(ctx, "peon-995-8")
	if !ok || sp.Workspace != ws.ID || sp.Bead != "beads-995.8" || sp.Ended {
		t.Fatalf("spawn record = %+v ok=%v", sp, ok)
	}
	unread, _ := r.lb.Unread(ctx, "peon-995-8")
	if len(unread) != 1 || unread[0].From != "chief-peons" || !strings.Contains(unread[0].Body, "spawn and end peons") || !strings.Contains(unread[0].Body, "Build pns spawn.") {
		t.Fatalf("brief = %+v", unread)
	}
	start := r.h.started[0]
	if start[0] != ws.Pane || start[1] != "peon-995-8" || !slices.Contains(start, "--name") || !strings.HasPrefix(start[len(start)-1], "[pns]") {
		t.Fatalf("start = %q", start)
	}
}

func TestSecondSpawnWhileLiveIsRefused(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.fleet.Spawn(ctx, "beads-995.8"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.fleet.Spawn(ctx, "beads-995.8"); !errors.Is(err, peon.ErrRunning) {
		t.Fatalf("err = %v, want ErrRunning", err)
	}
	if len(r.h.workspaces) != 1 {
		t.Fatalf("workspaces = %d, want 1", len(r.h.workspaces))
	}
}

func TestSpawnResumesADeadPeonInItsRecordedWorkspace(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.fleet.Spawn(ctx, "beads-995.8")
	r.h.exit("peon-995-8")
	if _, err := r.fleet.Spawn(ctx, "beads-995.8"); err != nil {
		t.Fatal(err)
	}
	if len(r.h.workspaces) != 1 || len(r.h.started) != 2 || r.h.started[1][0] != r.h.workspaces[0].Pane {
		t.Fatalf("workspaces=%d started=%q; want a restart in the same pane", len(r.h.workspaces), r.h.started)
	}
}

func TestFailedStartIsHealedBySpawningAgain(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.failStart = errors.New("agent_not_ready")
	if _, err := r.fleet.Spawn(ctx, "beads-995.8"); err == nil {
		t.Fatal("spawn succeeded, want the start error")
	}
	r.h.failStart = nil
	if _, err := r.fleet.Spawn(ctx, "beads-995.8"); err != nil {
		t.Fatal(err)
	}
	if len(r.h.workspaces) != 1 {
		t.Fatalf("workspaces = %d, want the first one reused", len(r.h.workspaces))
	}
}

func TestUnrecordedWorkspaceForTheBeadIsAnOrphan(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.workspaces = []peon.Workspace{{ID: "w42", Label: "beads-995.8"}}
	if _, err := r.fleet.Spawn(ctx, "beads-995.8"); !errors.Is(err, peon.ErrOrphan) {
		t.Fatalf("err = %v, want ErrOrphan", err)
	}
	if len(r.h.removed) != 0 || len(r.h.workspaces) != 1 {
		t.Fatalf("removed=%v workspaces=%v; orphans must be left alone", r.h.removed, r.h.workspaces)
	}
}

func TestBeadMustBelongToTheProject(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	for _, bead := range []string{"beads-7qx", "beads-nol"} {
		if _, err := r.fleet.Spawn(ctx, bead); !errors.Is(err, peon.ErrWrongRepo) {
			t.Errorf("%s: err = %v, want ErrWrongRepo", bead, err)
		}
	}
	r.fleet.ForeignRepo = true
	if _, err := r.fleet.Spawn(ctx, "beads-7qx"); err != nil {
		t.Fatalf("with ForeignRepo: %v", err)
	}
}

func TestEndRemovesOnlyTheRecordedWorkspace(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if err := r.fleet.End(ctx, "peon-995-8"); !errors.Is(err, peon.ErrNotSpawned) {
		t.Fatalf("never spawned: err = %v, want ErrNotSpawned", err)
	}
	r.fleet.Spawn(ctx, "beads-995.8")
	recorded := r.h.workspaces[0].ID
	r.h.workspaces = append(r.h.workspaces, peon.Workspace{ID: "w-other", Label: "beads-995.8"}) // a decoy
	for range 2 {
		if err := r.fleet.End(ctx, "peon-995-8"); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(r.h.removed, []string{recorded}) {
		t.Fatalf("removed = %v, want only %s", r.h.removed, recorded)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "peon-995-8"); !sp.Ended {
		t.Fatalf("spawn not marked ended: %+v", sp)
	}
}
