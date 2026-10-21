package worker_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/worker"
)

// plainRig is a rig whose workers get kind none, with hooks that log their env to a
// file and make (init) or remove (finalize) a directory per worker under clients.
type plainRig struct {
	rig
	clients, log string
}

// toyInit makes a stand-in CitC client per worker and prints it; rerun, it prints the
// same one. toyFinalize answers BS_CHECK with 3 while the client holds a file named
// work, and otherwise removes the client.
const (
	toyInit     = `echo "init $BS_NAME $BS_PROJECT $BS_REPO $BS_BASE dir=$BS_DIR" >> "$LOG"; mkdir -p "$CLIENTS/$BS_NAME" && echo "$CLIENTS/$BS_NAME"`
	toyFinalize = `echo "finalize $BS_NAME dir=$BS_DIR force=$BS_FORCE check=$BS_CHECK" >> "$LOG"; if [ -n "$BS_CHECK" ]; then [ -e "$BS_DIR/work" ] && { echo "work in $BS_DIR" >&2; exit 3; }; exit 0; fi; rm -rf "$BS_DIR"`
)

func newPlainRig(t *testing.T) plainRig {
	t.Helper()
	r := plainRig{rig: newRig(t)}
	dir := filepath.Dir(r.repo)
	r.clients, r.log = filepath.Join(dir, "clients"), filepath.Join(dir, "hooks.log")
	t.Setenv("CLIENTS", r.clients)
	t.Setenv("LOG", r.log)
	if err := os.MkdirAll(r.repo, 0o755); err != nil {
		t.Fatal(err)
	}
	r.fleet.WS = worker.WorkspaceSpec{Kind: worker.NoWorkspace, Init: toyInit, Finalize: toyFinalize}
	return r
}

func (r plainRig) hookLog(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(r.log)
	return string(b)
}

// endedData reads the data of name's ended entry straight from the logbook file.
func endedData(t *testing.T, r rig, name string) string {
	t.Helper()
	db, err := sql.Open("sqlite", r.fleet.P.Logbook)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var data sql.NullString
	if err := db.QueryRow(`SELECT data FROM entries WHERE kind = 'ended' AND subject = ?`, name).Scan(&data); err != nil {
		t.Fatal(err)
	}
	return data.String
}

func TestNoneOpensAPlainWorkspaceOnTheDirInitPrints(t *testing.T) {
	ctx := context.Background()
	r := newPlainRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "toy client"); err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(r.clients, "bees-8")
	ws := r.h.workspaces[0]
	if ws.Path != client || ws.Worker != "bees-8" || ws.Label != "bees · toy client" {
		t.Fatalf("workspace = %+v, want on %s, tagged bees-8", ws, client)
	}
	if env := r.h.env[ws.ID]; env["BS_PROJECT"] != "bees" || env["BS_LOGBOOK_DIR"] != filepath.Dir(r.fleet.P.Logbook) || env["BS_LOGBOOK"] != "" {
		t.Fatalf("workspace env = %v", env)
	}
	sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8")
	if sp.Kind != worker.NoWorkspace || sp.Dir != client || sp.Worktree != "" || sp.Branch != "" || sp.Init == nil || sp.Init.Exit != 0 {
		t.Fatalf("spawn record = %+v", sp)
	}
	if want := "init bees-8 bees " + r.repo + " " + r.repo + " dir=\n"; r.hookLog(t) != want {
		t.Fatalf("hook log = %q, want %q", r.hookLog(t), want)
	}
	if len(r.h.started) != 1 || r.h.started[0][0] != ws.Pane {
		t.Fatalf("started = %q", r.h.started)
	}
	start := r.h.started[0]
	settings, err := os.ReadFile(start[slices.Index(start, "--settings")+1])
	if err != nil || !strings.Contains(string(settings), "working in the directory "+client) || strings.Contains(string(settings), "branch") {
		t.Fatalf("settings = %s, %v; want the directory and no branch", settings, err)
	}
}

func TestNoneWithoutInitOpensOnTheRepo(t *testing.T) {
	r := newPlainRig(t)
	r.fleet.WS.Init = ""
	if _, err := r.fleet.Spawn(context.Background(), "bees-8", "shared checkout"); err != nil {
		t.Fatal(err)
	}
	if ws := r.h.workspaces[0]; ws.Path != r.repo || ws.Worker != "bees-8" {
		t.Fatalf("workspace = %+v, want on the repo %s", ws, r.repo)
	}
}

func TestNoneFailedInitRecordsNothing(t *testing.T) {
	ctx := context.Background()
	r := newPlainRig(t)
	r.fleet.WS.Init = `echo "no quota for a client" >&2; exit 7`
	_, err := r.fleet.Spawn(ctx, "bees-8", "toy client")
	if err == nil || !strings.Contains(err.Error(), "init hook exited 7: no quota for a client") {
		t.Fatalf("err = %v", err)
	}
	if _, ok, _ := r.lb.LatestSpawn(ctx, "bees-8"); ok || len(r.h.workspaces) != 0 || strings.Contains(r.hookLog(t), "finalize") {
		t.Fatalf("recorded = %v, workspaces = %v, hook log = %q; want nothing", ok, r.h.workspaces, r.hookLog(t))
	}
}

func TestNoneSpawnFailingAfterInitRunsFinalize(t *testing.T) {
	ctx := context.Background()
	for name, init := range map[string]string{
		"a missing dir":     `mkdir -p "$CLIENTS/made"; echo "$CLIENTS/gone"`,
		"a relative path":   `echo clients/bees-8`,
		"two lines":         `echo /tmp; echo /tmp`,
		"nothing":           `true`,
		"a file, not a dir": `f="$CLIENTS/file"; mkdir -p "$CLIENTS"; : > "$f"; echo "$f"`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newPlainRig(t)
			r.fleet.WS.Init = init
			if _, err := r.fleet.Spawn(ctx, "bees-8", "toy client"); err == nil {
				t.Fatal("spawn succeeded")
			}
			if _, ok, _ := r.lb.LatestSpawn(ctx, "bees-8"); ok || len(r.h.workspaces) != 0 || len(r.h.started) != 0 {
				t.Fatalf("recorded = %v, workspaces = %v; want nothing (herdr would open $HOME)", ok, r.h.workspaces)
			}
			if !strings.Contains(r.hookLog(t), "finalize bees-8") {
				t.Fatalf("hook log = %q, want finalize to have run", r.hookLog(t))
			}
		})
	}
}

func TestNoneEndChecksClosesFinalizesThenRecords(t *testing.T) {
	ctx := context.Background()
	r := newPlainRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "toy client"); err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(r.clients, "bees-8")
	if err := os.WriteFile(filepath.Join(client, "work"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := r.fleet.End(ctx, "bees-8", worker.EndOpts{})
	if !errors.Is(err, worker.ErrDirty) || !strings.Contains(err.Error(), "work in "+client) {
		t.Fatalf("end of a dirty client = %v, want ErrDirty with the hook's reason", err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); sp.Ended || len(r.h.closed) != 0 {
		t.Fatalf("dirty end: ended = %v, closed = %v; want nothing touched", sp.Ended, r.h.closed)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(client); !os.IsNotExist(err) || len(r.h.closed) != 1 {
		t.Fatalf("client stat = %v, closed = %v; want both gone", err, r.h.closed)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); !sp.Ended {
		t.Fatal("not recorded as ended")
	}
	if d := endedData(t, r.rig, "bees-8"); d != `{"finalize":{"exit":0}}` {
		t.Fatalf("ended data = %s", d)
	}
	if !strings.Contains(r.hookLog(t), "finalize bees-8 dir="+client+" force=1 check=\n") {
		t.Fatalf("hook log = %q", r.hookLog(t))
	}
}

func TestFailedFinalizeKeepsTheWorkerLiveAndEndingAgainRerunsIt(t *testing.T) {
	ctx := context.Background()
	r := newPlainRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "toy client"); err != nil {
		t.Fatal(err)
	}
	good := r.fleet.WS.Finalize
	r.fleet.WS.Finalize = `[ -n "$BS_CHECK" ] && exit 0; echo "client server down" >&2; exit 1`
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err == nil || !strings.Contains(err.Error(), "client server down") {
		t.Fatalf("err = %v", err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); sp.Ended {
		t.Fatal("a failed finalize recorded the end")
	}
	r.fleet.WS.Finalize = good
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.clients, "bees-8")); !os.IsNotExist(err) {
		t.Fatalf("client left behind: %v", err)
	}
}

func TestNoneEndLeavesAWorkspaceNotTaggedAsTheWorker(t *testing.T) {
	ctx := context.Background()
	r := newPlainRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "toy client"); err != nil {
		t.Fatal(err)
	}
	r.h.workspaces[0].Worker = "" // herdr reused the id for a workspace bs didn't open
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{Force: true}); !errors.Is(err, worker.ErrNotOurs) {
		t.Fatalf("err = %v, want ErrNotOurs", err)
	}
	if len(r.h.closed) != 0 || strings.Contains(r.hookLog(t), "finalize") {
		t.Fatalf("closed = %v, hook log = %q; want nothing touched", r.h.closed, r.hookLog(t))
	}
}

func TestNoneFindsAnOrphanByItsWorkerTag(t *testing.T) {
	r := newPlainRig(t)
	r.h.workspaces = append(r.h.workspaces, worker.Workspace{ID: "w9", Pane: "w9:p1", Worker: "bees-8"})
	if _, err := r.fleet.Spawn(context.Background(), "bees-8", "toy client"); !errors.Is(err, worker.ErrOrphan) || !strings.Contains(err.Error(), "herdr workspace close w9") {
		t.Fatalf("err = %v, want ErrOrphan naming w9", err)
	}
}

func TestNoneEndsAWorkerOnAPathHerdrCut(t *testing.T) {
	ctx := context.Background()
	r := newPlainRig(t)
	long := filepath.Join(r.clients, strings.Repeat("deep/", 20))
	r.fleet.WS.Init = `mkdir -p "` + long + `" && echo "` + long + `"`
	if _, err := r.fleet.Spawn(ctx, "bees-8", "toy client"); err != nil {
		t.Fatal(err)
	}
	if len(r.h.workspaces[0].Path) != 80 {
		t.Fatalf("path token = %q, want herdr's 80-character cut", r.h.workspaces[0].Path)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{Force: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.hookLog(t), "dir="+long+" ") {
		t.Fatalf("finalize didn't get the recorded dir: %q", r.hookLog(t))
	}
}

func TestInitRunsInTheNewWorktreeAndIsRecorded(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.fleet.WS.Init = `pwd > init-ran; echo "npm noise" >&2`
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	wt := r.repo + "-WT/bees-8"
	if b, err := os.ReadFile(filepath.Join(wt, "init-ran")); err != nil || strings.TrimSpace(string(b)) != wt {
		t.Fatalf("init ran in %q, %v; want %s", b, err, wt)
	}
	sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8")
	if sp.Init == nil || *sp.Init != (logbook.Hook{Exit: 0, Stderr: "npm noise"}) || sp.Kind != "" {
		t.Fatalf("spawn record = %+v", sp)
	}
}

func TestFailedInitTakesBackTheWorktree(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.fleet.WS.Init = `exit 2`
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err == nil {
		t.Fatal("spawn succeeded")
	}
	if _, ok, _ := r.lb.LatestSpawn(ctx, "bees-8"); ok || len(r.h.workspaces) != 0 || len(r.h.removed) != 1 {
		t.Fatalf("recorded = %v, workspaces = %v, removed = %v; want the worktree taken back", ok, r.h.workspaces, r.h.removed)
	}
	r.fleet.WS.Init = ""
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatalf("spawning again: %v", err)
	}
}

func TestFinalizeUnderGitWorktreeRunsAfterTheDirtyCheck(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	dir := filepath.Dir(r.repo)
	r.fleet.WS.Finalize = `echo "$BS_DIR" > "` + filepath.Join(dir, "finalized") + `"`
	if err := os.MkdirAll(r.repo, 0o755); err != nil { // finalize runs in the repo
		t.Fatal(err)
	}
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	wt := r.repo + "-WT/bees-8"
	gitRun(t, wt, "init", "-q")
	if err := os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); !errors.Is(err, worker.ErrDirty) {
		t.Fatalf("err = %v, want ErrDirty", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "finalized")); !os.IsNotExist(err) || len(r.h.removed) != 0 {
		t.Fatalf("finalize ran or the worktree went before the dirty check refused: %v, %v", err, r.h.removed)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{Force: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "finalized")); strings.TrimSpace(string(b)) != wt || len(r.h.removed) != 1 {
		t.Fatalf("finalize got %q, removed = %v", b, r.h.removed)
	}
}
