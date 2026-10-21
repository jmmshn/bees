package worker_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/worker"
)

// newPlainRig is a rig whose first spawns get workspace kind none, on a repo that is no
// git checkout (a stand-in for an hg or jj one).
func newPlainRig(t *testing.T) rig {
	t.Helper()
	r := newRig(t)
	r.repo = filepath.Join(filepath.Dir(r.repo), "hgrepo")
	if err := os.MkdirAll(filepath.Join(r.repo, ".hg"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.fleet.P.Repo = r.repo
	r.fleet.WorkspaceKind = worker.NoWorkspace
	return r
}

func TestNoneOpensATaggedPlainWorkspaceOnTheRepo(t *testing.T) {
	ctx := context.Background()
	r := newPlainRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "hg client"); err != nil {
		t.Fatal(err)
	}
	ws := r.h.workspaces[0]
	if ws.Worker != "bees-8" || ws.Label != "bees · hg client" || !slices.Equal(r.h.tagged, []string{r.repo}) {
		t.Fatalf("workspace = %+v, tagged %q; want on %s, tagged bees-8", ws, r.h.tagged, r.repo)
	}
	sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8")
	if sp.Kind != worker.NoWorkspace || sp.Dir != r.repo || sp.Worktree != "" || sp.Branch != "" || sp.Label != "hg client" {
		t.Fatalf("spawn record = %+v", sp)
	}
	start := r.h.started[0]
	if start[0] != ws.Pane {
		t.Fatalf("started in %s, want %s", start[0], ws.Pane)
	}
	settings, err := os.ReadFile(start[slices.Index(start, "--settings")+1])
	if err != nil || !strings.Contains(string(settings), "working in the directory "+r.repo) || strings.Contains(string(settings), "branch") {
		t.Fatalf("settings = %s, %v; want the directory and no branch", settings, err)
	}
}

func TestNoneRefusesAMissingRepoBeforeOpeningAnything(t *testing.T) {
	ctx := context.Background()
	r := newPlainRig(t)
	r.fleet.P.Repo = filepath.Join(r.repo, "gone") // herdr would open $HOME
	if _, err := r.fleet.Spawn(ctx, "bees-8", "hg client"); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v", err)
	}
	if _, ok, _ := r.lb.LatestSpawn(ctx, "bees-8"); ok || len(r.h.workspaces) != 0 {
		t.Fatalf("recorded = %v, workspaces = %v; want nothing", ok, r.h.workspaces)
	}
}

func TestNoneWorkersShareTheRepo(t *testing.T) {
	r := newPlainRig(t)
	res := r.fleet.SpawnAll(context.Background(), []worker.Request{{Bead: "bees-8", Label: "one"}, {Bead: "bees-9", Label: "two"}})
	for _, x := range res {
		if x.Err != nil {
			t.Fatalf("%s: %v", x.Bead, x.Err)
		}
	}
	if len(r.h.workspaces) != 2 || !slices.Equal(r.h.tagged, []string{r.repo, r.repo}) {
		t.Fatalf("workspaces = %+v, tagged %q; want two on the repo", r.h.workspaces, r.h.tagged)
	}
}

func TestNoneEndOnlyClosesTheWorkspace(t *testing.T) {
	ctx := context.Background()
	r := newPlainRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "hg client"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.repo, "work"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
		t.Fatal(err)
	}
	if len(r.h.closed) != 1 || len(r.h.removed) != 0 {
		t.Fatalf("closed = %v, removed = %v; want only the workspace closed", r.h.closed, r.h.removed)
	}
	if _, err := os.Stat(filepath.Join(r.repo, "work")); err != nil {
		t.Fatalf("the keeper's directory lost its work: %v", err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); !sp.Ended {
		t.Fatal("not recorded as ended")
	}
	if pruned, err := r.fleet.Prune(ctx, false); err != nil || len(pruned) != 0 {
		t.Fatalf("prune = %+v, %v; want nothing: kind none has no branch", pruned, err)
	}
}

func TestNoneEndLeavesAWorkspaceNotTaggedAsTheWorker(t *testing.T) {
	ctx := context.Background()
	r := newPlainRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "hg client"); err != nil {
		t.Fatal(err)
	}
	r.h.workspaces[0].Worker = "" // herdr reused the id for a workspace bs didn't open
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{Force: true}); !errors.Is(err, worker.ErrNotOurs) {
		t.Fatalf("err = %v, want ErrNotOurs", err)
	}
	if len(r.h.closed) != 0 {
		t.Fatalf("closed = %v; want nothing touched", r.h.closed)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{Forget: true}); err != nil || len(r.h.closed) != 0 {
		t.Fatalf("--forget: %v, closed = %v; want the end recorded and the workspace left", err, r.h.closed)
	}
}

func TestNoneFindsAnOrphanByItsWorkerTag(t *testing.T) {
	r := newPlainRig(t)
	r.h.workspaces = append(r.h.workspaces, worker.Workspace{ID: "w90", Pane: "w90:p1", Worker: "bees-9"})
	if _, err := r.fleet.Spawn(context.Background(), "bees-8", "hg client"); err != nil {
		t.Fatalf("another worker's workspace on the shared repo blocked the spawn: %v", err)
	}
	r.h.workspaces = append(r.h.workspaces, worker.Workspace{ID: "w91", Pane: "w91:p1", Worker: "bees-10"})
	if _, err := r.fleet.Spawn(context.Background(), "bees-10", "third"); !errors.Is(err, worker.ErrOrphan) || !strings.Contains(err.Error(), "herdr workspace close w91") {
		t.Fatalf("err = %v, want ErrOrphan naming w91", err)
	}
}

func TestNoneEndsAWorkerOnAPathLongerThanAToken(t *testing.T) {
	ctx := context.Background()
	r := newPlainRig(t)
	long := filepath.Join(r.repo, strings.Repeat("deep/", 20))
	if err := os.MkdirAll(long, 0o755); err != nil {
		t.Fatal(err)
	}
	r.fleet.P.Repo = long
	if _, err := r.fleet.Spawn(ctx, "bees-8", "hg client"); err != nil {
		t.Fatal(err)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil || len(r.h.closed) != 1 {
		t.Fatalf("end = %v, closed = %v", err, r.h.closed)
	}
}

func TestRecordedKindBeatsTheConfig(t *testing.T) {
	ctx := context.Background()
	r := newPlainRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "hg client"); err != nil {
		t.Fatal(err)
	}
	r.fleet.WorkspaceKind = worker.GitWorktree // the keeper switched kinds since
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil || len(r.h.closed) != 1 || len(r.h.removed) != 0 {
		t.Fatalf("end = %v, closed = %v, removed = %v; want the recorded kind's teardown", err, r.h.closed, r.h.removed)
	}
}

func TestNoneSpawnThatCantTagClosesItsWorkspace(t *testing.T) {
	ctx := context.Background()
	r := newPlainRig(t)
	r.h.tagErr = errors.New("herdr workspace report-metadata: refused")
	if _, err := r.fleet.Spawn(ctx, "bees-8", "hg client"); err == nil {
		t.Fatal("spawn succeeded with an untagged workspace")
	}
	if _, ok, _ := r.lb.LatestSpawn(ctx, "bees-8"); ok || len(r.h.workspaces) != 0 || len(r.h.closed) != 1 {
		t.Fatalf("workspaces = %v, closed = %v; want the workspace taken back", r.h.workspaces, r.h.closed)
	}
}
