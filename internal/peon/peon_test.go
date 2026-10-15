package peon_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
		"peons-8":    {ID: "peons-8", Title: "spawn and end peons", Description: "Build pns spawn."},
		"dotfiles-3": {ID: "dotfiles-3", Title: "dotfiles thing"},
		"beads-x7q":  {ID: "beads-x7q", Title: "flat id"},
	}
	repo := filepath.Join(dir, "peons")
	p := project.Project{Name: "peons", Repo: repo, Logbook: filepath.Join(dir, "peons.db")}
	return rig{fleet: peon.Fleet{P: p, LB: lb, H: h, B: beads, Me: "chief-peons"}, h: h, lb: lb, repo: repo}
}

func TestName(t *testing.T) {
	for bead, want := range map[string]string{"peons-8": "peons-8", "peons-12.3": "peons-12-3", "pymatgen_analysis_defects-12.3": "pymatgen_analysis_defects-12-3"} {
		if got, err := peon.Name(bead); err != nil || got != want {
			t.Errorf("Name(%q) = %q, %v; want %q", bead, got, err, want)
		}
	}
	for _, bad := range []string{"peons-1" + strings.Repeat(".1", 14), "", "Peons-1"} {
		if _, err := peon.Name(bad); err == nil {
			t.Errorf("Name(%q) succeeded, want error", bad)
		}
	}
}

func TestBelongs(t *testing.T) {
	for _, c := range []struct {
		project, bead string
		want          bool
	}{
		{"peons", "peons-8", true}, {"peons", "peons-12.3.4", true},
		{"peons", "peons-x", false}, {"peons", "peons-8.", false}, {"peons", "beads-995.8", false},
		{"web", "web_2-3", false}, {"pymatgen", "pymatgen_analysis_defects-3", false},
	} {
		if got := peon.Belongs(c.project, c.bead); got != c.want {
			t.Errorf("Belongs(%q, %q) = %v, want %v", c.project, c.bead, got, c.want)
		}
	}
}

func TestNextEpic(t *testing.T) {
	ids := []string{"peons-1", "peons-1.4", "peons-9", "peons-10.2", "dotfiles-40", "beads-995"}
	if got := peon.NextEpic("peons", ids); got != "peons-11" {
		t.Fatalf("NextEpic = %q, want peons-11", got)
	}
	if got := peon.NextEpic("crew", ids); got != "crew-1" {
		t.Fatalf("NextEpic(empty project) = %q, want crew-1", got)
	}
}

func TestSpawnStartsAPeonInItsOwnWorkspace(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	st, err := r.fleet.Spawn(ctx, "peons-8")
	if err != nil {
		t.Fatal(err)
	}
	if st.Name != "peons-8" || st.Resumed || st.Waiting {
		t.Fatalf("started = %+v", st)
	}
	if slices.Contains(r.h.started[0], "--continue") {
		t.Fatalf("fresh start passed --continue: %q", r.h.started[0])
	}
	wt := r.repo + "-WT/peons-8"
	ws := r.h.workspaces[0]
	if ws.Path != wt || ws.Label != "peons-8" {
		t.Fatalf("workspace = %+v", ws)
	}
	if _, err := os.Stat(filepath.Join(wt, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("spawn wrote into the worktree's .claude (Claude reads settings from the main checkout): %v", err)
	}
	sp, ok, _ := r.lb.LatestSpawn(ctx, "peons-8")
	if !ok || sp.Workspace != ws.ID || sp.Bead != "peons-8" || sp.Ended {
		t.Fatalf("spawn record = %+v ok=%v", sp, ok)
	}
	unread, _ := r.lb.Unread(ctx, "peons-8")
	if len(unread) != 1 || unread[0].From != "chief-peons" || !strings.Contains(unread[0].Body, "spawn and end peons") || !strings.Contains(unread[0].Body, "Build pns spawn.") {
		t.Fatalf("brief = %+v", unread)
	}
	start := r.h.started[0]
	if start[0] != ws.Pane || start[1] != "peons-8" || !strings.HasPrefix(start[len(start)-1], "[pns]") {
		t.Fatalf("start = %q", start)
	}
	if i := slices.Index(start, "--name"); i < 0 || start[i+1] != "peons-8" {
		t.Fatalf("start = %q, want --name peons-8", start)
	}
	i := slices.Index(start, "--settings")
	if i < 0 {
		t.Fatalf("start = %q, want --settings", start)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(start[i+1]), &settings); err != nil || settings["crossSessionInbound"] != "accept" {
		t.Fatalf("--settings %q: %v", start[i+1], err)
	}
}

func TestSecondSpawnWhileLiveIsRefused(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.fleet.Spawn(ctx, "peons-8"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.fleet.Spawn(ctx, "peons-8"); !errors.Is(err, peon.ErrRunning) {
		t.Fatalf("err = %v, want ErrRunning", err)
	}
	if len(r.h.workspaces) != 1 {
		t.Fatalf("workspaces = %d, want 1", len(r.h.workspaces))
	}
}

func TestSpawnResumesADeadPeonInItsRecordedWorkspace(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.fleet.Spawn(ctx, "peons-8")
	r.h.exit("peons-8")
	if _, err := r.fleet.Spawn(ctx, "peons-8"); err != nil {
		t.Fatal(err)
	}
	if len(r.h.workspaces) != 1 || len(r.h.started) != 2 || r.h.started[1][0] != r.h.workspaces[0].Pane {
		t.Fatalf("workspaces=%d started=%q; want a restart in the same pane", len(r.h.workspaces), r.h.started)
	}
}

func TestResumeContinuesThePeonsConversation(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.fleet.Spawn(ctx, "peons-8")
	r.h.exit("peons-8")
	st, err := r.fleet.Spawn(ctx, "peons-8")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Resumed || st.Workspace != r.h.workspaces[0].ID {
		t.Fatalf("started = %+v, want resumed in %s", st, r.h.workspaces[0].ID)
	}
	if !slices.Contains(r.h.started[1], "--continue") {
		t.Fatalf("resume args = %q, want --continue", r.h.started[1])
	}
}

func TestPeonWaitingAtAPromptCountsAsStarted(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.failStart = fmt.Errorf("herdr agent start: blocked during startup: %w", peon.ErrWaiting)
	st, err := r.fleet.Spawn(ctx, "peons-8")
	if err != nil || !st.Waiting || st.Name != "peons-8" {
		t.Fatalf("started = %+v, err = %v; want waiting, no error", st, err)
	}
}

func TestFailedStartIsHealedBySpawningAgain(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.failStart = errors.New("agent_not_ready")
	if _, err := r.fleet.Spawn(ctx, "peons-8"); err == nil {
		t.Fatal("spawn succeeded, want the start error")
	}
	r.h.failStart = nil
	if _, err := r.fleet.Spawn(ctx, "peons-8"); err != nil {
		t.Fatal(err)
	}
	if len(r.h.workspaces) != 1 {
		t.Fatalf("workspaces = %d, want the first one reused", len(r.h.workspaces))
	}
}

func TestUnrecordedWorkspaceForTheBeadIsAnOrphan(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.workspaces = []peon.Workspace{{ID: "w42", Label: "peons-8"}}
	_, err := r.fleet.Spawn(ctx, "peons-8")
	if !errors.Is(err, peon.ErrOrphan) || !strings.Contains(err.Error(), "herdr worktree remove --workspace w42 --force") {
		t.Fatalf("err = %v, want ErrOrphan with the cleanup command", err)
	}
	if len(r.h.removed) != 0 || len(r.h.workspaces) != 1 {
		t.Fatalf("removed=%v workspaces=%v; orphans must be left alone", r.h.removed, r.h.workspaces)
	}
}

func TestBeadMustBelongToTheProject(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	for _, bead := range []string{"dotfiles-3", "beads-x7q"} {
		if _, err := r.fleet.Spawn(ctx, bead); !errors.Is(err, peon.ErrWrongRepo) {
			t.Errorf("%s: err = %v, want ErrWrongRepo", bead, err)
		}
	}
	r.fleet.ForeignRepo = true
	if _, err := r.fleet.Spawn(ctx, "dotfiles-3"); err != nil {
		t.Fatalf("with ForeignRepo: %v", err)
	}
}

func TestEndRemovesOnlyTheRecordedWorkspace(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.fleet.End(ctx, "peons-8"); !errors.Is(err, peon.ErrNotSpawned) {
		t.Fatalf("never spawned: err = %v, want ErrNotSpawned", err)
	}
	r.fleet.Spawn(ctx, "peons-8")
	recorded := r.h.workspaces[0].ID
	r.h.workspaces = append(r.h.workspaces, peon.Workspace{ID: "w-other", Label: "peons-8"}) // a decoy
	for range 2 {
		if _, err := r.fleet.End(ctx, "peons-8"); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(r.h.removed, []string{recorded}) {
		t.Fatalf("removed = %v, want only %s", r.h.removed, recorded)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "peons-8"); !sp.Ended {
		t.Fatalf("spawn not marked ended: %+v", sp)
	}
}

func TestEndDeletesTheBranchOnlyIfMerged(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	gitRun(t, "", "init", "-q", "-b", "main", r.repo)
	gitRun(t, r.repo, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "init")
	r.fleet.Spawn(ctx, "peons-8")
	gitRun(t, r.repo, "branch", "peons-8") // merged: no commits of its own
	if kept, err := r.fleet.End(ctx, "peons-8"); err != nil || kept {
		t.Fatalf("merged branch: kept=%v err=%v", kept, err)
	}
	r.h.exit("peons-8")
	r.fleet.Spawn(ctx, "peons-8")
	gitRun(t, r.repo, "checkout", "-q", "-b", "peons-8")
	gitRun(t, r.repo, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "work")
	gitRun(t, r.repo, "checkout", "-q", "main")
	if kept, err := r.fleet.End(ctx, "peons-8"); err != nil || !kept {
		t.Fatalf("unmerged branch: kept=%v err=%v", kept, err)
	}
}

func TestSpawnRecordsTheBriefWithTheSpawn(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.failStart = errors.New("crash after recording")
	r.fleet.Spawn(ctx, "peons-8")
	unread, err := r.lb.Unread(ctx, "peons-8")
	if err != nil || len(unread) != 1 {
		t.Fatalf("brief after a failed start: %+v, %v", unread, err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
