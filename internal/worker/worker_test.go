package worker_test

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
	"sync"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/project"
	"github.com/jmmshn/bees/internal/prompt"
	"github.com/jmmshn/bees/internal/worker"
)

// fakeHerdr keeps workspaces and live Claude sessions in memory, and records removals.
// Like herdr 0.9.3 it keeps any label verbatim, even empty or with newlines
// (TestHerdrKeepsLabelsVerbatim), so CheckLabel is bs's rule, not herdr's.
// It is also the fake Claude registry (worker.Sessions), so a start shows up there.
type fakeHerdr struct {
	mu         sync.Mutex // SpawnAll starts workers concurrently
	together   int        // >0: StartClaude waits until this many starts are in flight, else fails
	inFlight   int
	next       int
	workspaces []worker.Workspace
	agents     map[string]string // live Claude sessions: name → pane
	started    [][]string        // pane, name, args...
	removed    []string
	failStart  error
	liveAnyway bool  // with failStart: Claude came up all the same, as after herdr's timeout
	aliveErr   error // the registry can't be read
}

func (h *fakeHerdr) CreateWorktree(ctx context.Context, repo, branch, path, label string) (worker.Workspace, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return worker.Workspace{}, err
	}
	h.next++
	ws := worker.Workspace{ID: fmt.Sprintf("w%d", h.next), Label: label, Pane: fmt.Sprintf("w%d:p1", h.next), Path: path}
	h.workspaces = append(h.workspaces, ws)
	return ws, nil
}

func (h *fakeHerdr) StartClaude(ctx context.Context, pane, name string, args ...string) error {
	if err := worker.CheckAgentArgs(args); err != nil { // herdr's own rule, held to real herdr
		return err
	}
	if err := h.waitTogether(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failStart == nil || h.liveAnyway {
		h.started = append(h.started, append([]string{pane, name}, args...))
		h.agents[name] = pane
	}
	return h.failStart
}

// waitTogether holds a start until h.together starts are in flight: a serial spawn
// never gets there and times out.
func (h *fakeHerdr) waitTogether() error {
	if h.together == 0 {
		return nil
	}
	h.mu.Lock()
	h.inFlight++
	h.mu.Unlock()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		h.mu.Lock()
		n := h.inFlight
		h.mu.Unlock()
		if n >= h.together {
			return nil
		}
	}
	return fmt.Errorf("only %d of %d starts in flight together", h.inFlight, h.together)
}

func (h *fakeHerdr) Alive(ctx context.Context, name string, wait time.Duration) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.agents[name]
	return ok, h.aliveErr
}

func (h *fakeHerdr) RemoveWorktree(ctx context.Context, workspace string) error {
	h.removed = append(h.removed, workspace)
	h.workspaces = slices.DeleteFunc(h.workspaces, func(w worker.Workspace) bool { return w.ID == workspace })
	return nil
}

func (h *fakeHerdr) Workspaces(ctx context.Context) ([]worker.Workspace, error) {
	return h.workspaces, nil
}

// exit simulates the worker's Claude process ending.
func (h *fakeHerdr) exit(name string) { delete(h.agents, name) }

type fakeBeads map[string]worker.Bead

func (b fakeBeads) Show(ctx context.Context, id string) (worker.Bead, error) {
	bead, ok := b[id]
	if !ok {
		return worker.Bead{}, fmt.Errorf("no bead %s", id)
	}
	return bead, nil
}

type rig struct {
	fleet worker.Fleet
	h     *fakeHerdr
	lb    *logbook.Logbook
	repo  string
}

func newRig(t *testing.T) rig {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	lb, err := logbook.Open(ctx, filepath.Join(dir, "bees.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lb.Close() })
	h := &fakeHerdr{agents: map[string]string{}}
	beads := fakeBeads{
		"bees-8":     {ID: "bees-8", Title: "spawn and end bees", Description: "Build bs spawn."},
		"bees-9":     {ID: "bees-9", Title: "second worker"},
		"dotfiles-3": {ID: "dotfiles-3", Title: "dotfiles thing"},
		"beads-x7q":  {ID: "beads-x7q", Title: "flat id"},
	}
	repo := filepath.Join(dir, "bees")
	p := project.Project{Name: "bees", Repo: repo, Logbook: filepath.Join(dir, "bees.db")}
	return rig{fleet: worker.Fleet{P: p, LB: lb, H: h, S: h, B: beads, Me: "queen-bees"}, h: h, lb: lb, repo: repo}
}

func TestName(t *testing.T) {
	for bead, want := range map[string]string{"bees-8": "bees-8", "bees-12.3": "bees-12-3", "pymatgenanalysisdefects-12.3": "pymatgenanalysisdefects-12-3"} {
		if got, err := worker.Name(bead); err != nil || got != want {
			t.Errorf("Name(%q) = %q, %v; want %q", bead, got, err, want)
		}
	}
	for _, bad := range []string{"bees-1" + strings.Repeat(".1", 14), "", "Bees-1"} {
		if _, err := worker.Name(bad); err == nil {
			t.Errorf("Name(%q) succeeded, want error", bad)
		}
	}
}

func TestBelongs(t *testing.T) {
	for _, c := range []struct {
		project, bead string
		want          bool
	}{
		{"bees", "bees-8", true}, {"bees", "bees-12.3.4", true},
		{"bees", "bees-x", false}, {"bees", "bees-8.", false}, {"bees", "beads-995.8", false},
		{"web", "web2-3", false}, {"pymatgen", "pymatgenanalysisdefects-3", false},
	} {
		if got := worker.Belongs(c.project, c.bead); got != c.want {
			t.Errorf("Belongs(%q, %q) = %v, want %v", c.project, c.bead, got, c.want)
		}
	}
}

func TestNextEpic(t *testing.T) {
	ids := []string{"bees-1", "bees-1.4", "bees-9", "bees-10.2", "dotfiles-40", "beads-995"}
	if got := worker.NextEpic("bees", ids); got != "bees-11" {
		t.Fatalf("NextEpic = %q, want bees-11", got)
	}
	if got := worker.NextEpic("crew", ids); got != "crew-1" {
		t.Fatalf("NextEpic(empty project) = %q, want crew-1", got)
	}
}

func TestSpawnStartsAWorkerInItsOwnWorkspace(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	st, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if err != nil {
		t.Fatal(err)
	}
	if st.Name != "bees-8" || st.Label != "spawn and end" || st.Resumed || st.Waiting {
		t.Fatalf("started = %+v", st)
	}
	if slices.Contains(r.h.started[0], "--continue") {
		t.Fatalf("fresh start passed --continue: %q", r.h.started[0])
	}
	wt := r.repo + "-WT/bees-8"
	ws := r.h.workspaces[0]
	if ws.Path != wt || ws.Label != "spawn and end" {
		t.Fatalf("workspace = %+v", ws)
	}
	if _, err := os.Stat(filepath.Join(wt, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("spawn wrote into the worktree's .claude (Claude reads settings from the main checkout): %v", err)
	}
	sp, ok, _ := r.lb.LatestSpawn(ctx, "bees-8")
	if !ok || sp.Workspace != ws.ID || sp.Bead != "bees-8" || sp.Label != "spawn and end" || sp.Ended {
		t.Fatalf("spawn record = %+v ok=%v", sp, ok)
	}
	unread, _ := r.lb.Unread(ctx, "bees-8")
	if len(unread) != 1 || unread[0].From != "queen-bees" || !strings.Contains(unread[0].Body, "spawn and end bees") || !strings.Contains(unread[0].Body, "Build bs spawn.") {
		t.Fatalf("brief = %+v", unread)
	}
	start := r.h.started[0]
	if start[0] != ws.Pane || start[1] != "bees-8" || !strings.HasPrefix(start[len(start)-1], "[bs]") {
		t.Fatalf("start = %q", start)
	}
	if i := slices.Index(start, "--name"); i < 0 || start[i+1] != "bees-8" {
		t.Fatalf("start = %q, want --name bees-8", start)
	}
	i := slices.Index(start, "--settings")
	if i < 0 {
		t.Fatalf("start = %q, want --settings", start)
	}
	if err := worker.CheckAgentArgs(start[2:]); err != nil {
		t.Fatalf("herdr would refuse the start: %v", err)
	}
	var settings struct {
		CrossSessionInbound string              `json:"crossSessionInbound"`
		AutoMode            map[string][]string `json:"autoMode"`
		Permissions         struct{ Deny []string }
	}
	if err := json.Unmarshal([]byte(start[i+1]), &settings); err != nil || settings.CrossSessionInbound != "accept" {
		t.Fatalf("--settings %q: %v", start[i+1], err)
	}
	// A list without $defaults would drop the built-in rules (force push, exfiltration).
	for _, k := range []string{"environment", "allow"} {
		l := settings.AutoMode[k]
		if !slices.Contains(l, "$defaults") || !strings.Contains(strings.Join(l, " "), "bees-8") {
			t.Errorf("autoMode.%s = %q, want $defaults and the worker's branch", k, l)
		}
	}
	if p := bsProject(t, start); p != "bees" {
		t.Errorf("env.BS_PROJECT = %q, want bees: the worker's bs must read the spawner's logbook", p)
	}
	if !strings.Contains(strings.Join(settings.AutoMode["environment"], " "), wt) {
		t.Errorf("autoMode.environment = %q, want the worktree %s", settings.AutoMode["environment"], wt)
	}
	for _, rule := range []string{"Bash(git push --force*)", "Bash(git push * main*)", "Bash(git -C * push * --force*)", "Bash(gh pr merge*)", "Bash(gh pr review* --approve*)"} {
		if !slices.Contains(settings.Permissions.Deny, rule) {
			t.Errorf("permissions.deny = %q, want %s", settings.Permissions.Deny, rule)
		}
	}
}

func TestSpawnAllStartsWorkersConcurrently(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.together = 2
	res := r.fleet.SpawnAll(ctx, []worker.Request{{Bead: "bees-8", Label: "spawn and end"}, {Bead: "bees-9", Label: "second worker"}})
	if len(res) != 2 || res[0].Bead != "bees-8" || res[1].Bead != "bees-9" {
		t.Fatalf("results = %+v, want one per bead in order", res)
	}
	for _, x := range res {
		if x.Err != nil || x.Started.Name == "" {
			t.Fatalf("result = %+v; want both starts in flight at once", x)
		}
		if _, ok, _ := r.lb.LatestSpawn(ctx, x.Started.Name); !ok {
			t.Fatalf("%s not recorded", x.Started.Name)
		}
	}
}

func TestSpawnAllStartsNothingIfAnyRequestIsRefused(t *testing.T) {
	ctx := context.Background()
	for _, reqs := range [][]worker.Request{
		{{Bead: "bees-8", Label: "spawn and end"}, {Bead: "bees-9"}},                         // a first spawn without a label
		{{Bead: "bees-8", Label: "spawn and end"}, {Bead: "bees-9", Label: "Bad!"}},          // a bad label
		{{Bead: "bees-8", Label: "spawn and end"}, {Bead: "bees-8", Label: "spawn and end"}}, // named twice: never two Claudes
	} {
		r := newRig(t)
		res := r.fleet.SpawnAll(ctx, reqs)
		if !errors.Is(res[0].Err, worker.ErrNotStarted) || !worker.Refused(res[1].Err) {
			t.Errorf("%+v: errs = %v, %v; want not started, refused", reqs, res[0].Err, res[1].Err)
		}
		if len(r.h.started) != 0 || len(r.h.workspaces) != 0 {
			t.Errorf("%+v: starts = %d, workspaces = %d; want nothing touched", reqs, len(r.h.started), len(r.h.workspaces))
		}
	}
}

func TestSpawnAllStartsTheRestPastAFailure(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	res := r.fleet.SpawnAll(ctx, []worker.Request{{Bead: "bees-404", Label: "no such bead"}, {Bead: "bees-8", Label: "spawn and end"}})
	if res[0].Err == nil || worker.Refused(res[0].Err) || res[1].Err != nil {
		t.Fatalf("errs = %v, %v; want bees-404 failed, bees-8 started", res[0].Err, res[1].Err)
	}
	if len(r.h.started) != 1 {
		t.Fatalf("starts = %d, want 1", len(r.h.started))
	}
}

func TestSpawnAllRepeatAfterARefusalIsNotTwice(t *testing.T) {
	r := newRig(t)
	res := r.fleet.SpawnAll(context.Background(), []worker.Request{{Bead: "bees-8", Label: "Bad!"}, {Bead: "bees-8", Label: "spawn and end"}})
	if !errors.Is(res[0].Err, worker.ErrBadLabel) || !errors.Is(res[1].Err, worker.ErrNotStarted) {
		t.Fatalf("errs = %v, %v; want the bad label alone blamed", res[0].Err, res[1].Err)
	}
}

func TestSecondSpawnWhileLiveIsRefused(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); !errors.Is(err, worker.ErrRunning) {
		t.Fatalf("err = %v, want ErrRunning", err)
	}
	if len(r.h.workspaces) != 1 {
		t.Fatalf("workspaces = %d, want 1", len(r.h.workspaces))
	}
}

func TestSpawnResumesADeadWorkerInItsRecordedWorkspace(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.exit("bees-8")
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	if len(r.h.workspaces) != 1 || len(r.h.started) != 2 || r.h.started[1][0] != r.h.workspaces[0].Pane {
		t.Fatalf("workspaces=%d started=%q; want a restart in the same pane", len(r.h.workspaces), r.h.started)
	}
}

func TestResumeContinuesTheWorkersConversation(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	readInbox(t, r.lb, "bees-8")
	r.h.exit("bees-8")
	st, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end")
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

func TestRestartOfAWorkerThatNeverReadItsBriefStartsFresh(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	// The start failed after spawn was recorded, so there is no conversation:
	// claude --continue would exit with "No conversation found to continue".
	r.h.failStart = errors.New("herdr agent: agent arguments cannot be encoded safely")
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.failStart = nil
	st, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if err != nil || !st.Resumed {
		t.Fatalf("started = %+v, err = %v; want a restart in the recorded workspace", st, err)
	}
	if start := r.h.started[0]; slices.Contains(start, "--continue") || !strings.HasPrefix(start[len(start)-1], "[bs]") {
		t.Fatalf("restart args = %q, want a fresh start with the doorbell", start)
	}
}

func TestEndedHalfStartedWorkerRespawnsWithOneBrief(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.failStart = errors.New("herdr agent: agent arguments cannot be encoded safely")
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if err := r.fleet.End(ctx, "bees-8"); err != nil {
		t.Fatal(err)
	}
	r.h.failStart = nil
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	if unread, _ := r.lb.Unread(ctx, "bees-8"); len(unread) != 1 {
		t.Fatalf("unread = %+v, want only the new spawn's brief", unread)
	}
}

func TestRestartInAnotherProjectsRepoContinuesOnlyAfterItsInbox(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.fleet.ForeignRepo, r.fleet.P.Repo = true, filepath.Join(t.TempDir(), "dotfiles")
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.exit("bees-8")
	// Its bs reads this logbook (BS_PROJECT), so its receipts say whether it has a conversation.
	if st, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil || st.Continued || slices.Contains(r.h.started[1], "--continue") {
		t.Fatalf("started = %+v, err = %v, args = %q; want a fresh start: it never read its inbox", st, err, r.h.started[1])
	}
	readInbox(t, r.lb, "bees-8")
	r.h.exit("bees-8")
	if st, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil || !st.Continued || !slices.Contains(r.h.started[2], "--continue") {
		t.Fatalf("started = %+v, err = %v, args = %q; want --continue", st, err, r.h.started[2])
	}
}

// readInbox does what the worker's bs inbox does: records receipts for its unread messages.
func readInbox(t *testing.T, lb *logbook.Logbook, name string) {
	t.Helper()
	ctx := context.Background()
	unread, err := lb.Unread(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	var ids []logbook.ID
	for _, m := range unread {
		ids = append(ids, m.ID)
	}
	if err := lb.RecordReceipts(ctx, name, ids); err != nil {
		t.Fatal(err)
	}
}

func TestWorkersStartWithTheProjectsSystemPrompt(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.exit("bees-8")
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	for _, start := range r.h.started {
		i := slices.Index(start, "--append-system-prompt-file")
		if i < 0 {
			t.Fatalf("start = %q, want internal/prompt/WORKER.md appended to the system prompt", start)
		}
		if b, err := os.ReadFile(start[i+1]); err != nil || string(b) != prompt.Text(prompt.Worker, "bees") {
			t.Fatalf("prompt file %s: %v\n%s", start[i+1], err, b)
		}
	}
}

// bees-1.54: a --repo worker's bs resolved the worktree's repo (dotfiles), so its bs send
// to queen-bees and its receipts landed in dotfiles.db while the queen read bees.db.
func TestAWorkerInAnotherProjectsRepoStaysInTheSpawnersProject(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	own := r.fleet
	r.fleet.ForeignRepo, r.fleet.P.Repo = true, filepath.Join(t.TempDir(), "dotfiles") // as --repo does
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.exit("bees-8")
	_, _ = own.Spawn(ctx, "bees-8", "spawn and end") // a resume doesn't repeat --repo
	if len(r.h.started) != 2 {
		t.Fatalf("started = %q, want a start and a restart", r.h.started)
	}
	for _, start := range r.h.started {
		if bsProject(t, start) != "bees" {
			t.Errorf("start = %q, want env.BS_PROJECT bees in --settings", start)
		}
		i := slices.Index(start, "--append-system-prompt-file")
		if i < 0 {
			t.Fatalf("start = %q, want the bees worker prompt", start)
		}
		if b, err := os.ReadFile(start[i+1]); err != nil || string(b) != prompt.Text(prompt.Worker, "bees") {
			t.Fatalf("prompt file %s: %v\n%s", start[i+1], err, b)
		}
	}
}

// bsProject returns env.BS_PROJECT from a start's --settings.
func bsProject(t *testing.T, start []string) string {
	t.Helper()
	i := slices.Index(start, "--settings")
	if i < 0 {
		t.Fatalf("start = %q, want --settings", start)
	}
	var settings struct{ Env map[string]string }
	if err := json.Unmarshal([]byte(start[i+1]), &settings); err != nil {
		t.Fatalf("--settings %q: %v", start[i+1], err)
	}
	return settings.Env["BS_PROJECT"]
}

func TestWorkerWaitingAtAPromptCountsAsStarted(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.failStart = fmt.Errorf("herdr agent start: blocked during startup: %w", worker.ErrWaiting)
	st, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if err != nil || !st.Waiting || st.Name != "bees-8" {
		t.Fatalf("started = %+v, err = %v; want waiting, no error", st, err)
	}
}

func TestHerdrTimeoutWithClaudeRunningCountsAsStarted(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	// herdr stops waiting while Claude is busy with its first prompt (bees-1.14).
	r.h.failStart, r.h.liveAnyway = fmt.Errorf("herdr agent: timed out waiting for agent startup: %w", worker.ErrUnconfirmed), true
	st, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if err != nil || st.Name != "bees-8" || st.Waiting {
		t.Fatalf("started = %+v, err = %v; want started", st, err)
	}
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); !errors.Is(err, worker.ErrRunning) {
		t.Fatalf("second spawn: err = %v, want ErrRunning (herdr never named it, but Claude runs)", err)
	}
}

func TestHerdrTimeoutWithNoClaudeIsAFailure(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.failStart = fmt.Errorf("herdr agent: timed out waiting for agent startup: %w", worker.ErrUnconfirmed)
	_, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if !errors.Is(err, worker.ErrUnconfirmed) || !strings.Contains(err.Error(), "herdr pane read w1:p1") {
		t.Fatalf("err = %v, want ErrUnconfirmed pointing at the pane", err)
	}
}

func TestUnreadableRegistryRefusesToRestart(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.exit("bees-8")
	r.h.aliveErr = errors.New("registry unreadable")
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err == nil || len(r.h.started) != 1 {
		t.Fatalf("err = %v, starts = %d; a failed liveness probe must not start a second Claude", err, len(r.h.started))
	}
}

func TestFailedStartIsHealedBySpawningAgain(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.failStart = errors.New("agent_not_ready")
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err == nil {
		t.Fatal("spawn succeeded, want the start error")
	}
	r.h.failStart = nil
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	if len(r.h.workspaces) != 1 {
		t.Fatalf("workspaces = %d, want the first one reused", len(r.h.workspaces))
	}
}

func TestUnrecordedWorkspaceOnTheWorkersWorktreeIsAnOrphan(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	// Found by its worktree whatever its label: a label is free text, not an identity.
	r.h.workspaces = []worker.Workspace{{ID: "w41", Label: "bees-8", Path: r.repo + "-WT/bees-9"}, {ID: "w42", Label: "something else", Path: r.repo + "-WT/bees-8/"}}
	_, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if !errors.Is(err, worker.ErrOrphan) || !strings.Contains(err.Error(), "herdr worktree remove --workspace w42 --force") {
		t.Fatalf("err = %v, want ErrOrphan with the cleanup command", err)
	}
	if len(r.h.removed) != 0 || len(r.h.workspaces) != 2 {
		t.Fatalf("removed=%v workspaces=%v; orphans must be left alone", r.h.removed, r.h.workspaces)
	}
}

func TestFirstSpawnNeedsALabel(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", ""); !errors.Is(err, worker.ErrNoLabel) {
		t.Fatalf("err = %v, want ErrNoLabel", err)
	}
	if _, err := r.fleet.Spawn(ctx, "bees-8", "Spawn-And-End."); !errors.Is(err, worker.ErrBadLabel) {
		t.Fatalf("err = %v, want ErrBadLabel", err)
	}
	if len(r.h.workspaces) != 0 {
		t.Fatalf("workspaces = %v; a refused spawn must not touch herdr", r.h.workspaces)
	}
	if _, ok, _ := r.lb.LatestSpawn(ctx, "bees-8"); ok {
		t.Fatal("a refused spawn was recorded")
	}
}

func TestRespawnKeepsTheRecordedLabel(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.exit("bees-8")
	if _, err := r.fleet.Spawn(ctx, "bees-8", "other words"); !errors.Is(err, worker.ErrLabelChange) || len(r.h.started) != 1 {
		t.Fatalf("err = %v, starts = %d; want ErrLabelChange and no start", err, len(r.h.started))
	}
	st, err := r.fleet.Spawn(ctx, "bees-8", "")
	if err != nil || !st.Resumed || st.Label != "spawn and end" {
		t.Fatalf("started = %+v, err = %v; want a resume with the recorded label", st, err)
	}
	if _, err := r.fleet.Spawn(ctx, "bees-8", ""); !errors.Is(err, worker.ErrRunning) {
		t.Fatalf("live worker: err = %v, want ErrRunning", err)
	}
}

func TestRespawnAfterEndReusesTheRecordedLabel(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if err := r.fleet.End(ctx, "bees-8"); err != nil {
		t.Fatal(err)
	}
	st, err := r.fleet.Spawn(ctx, "bees-8", "")
	if err != nil || st.Resumed || st.Label != "spawn and end" {
		t.Fatalf("started = %+v, err = %v; want a fresh spawn with the recorded label", st, err)
	}
	if ws := r.h.workspaces[len(r.h.workspaces)-1]; ws.Label != "spawn and end" {
		t.Fatalf("workspace label = %q, want the recorded one", ws.Label)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); sp.Label != "spawn and end" {
		t.Fatalf("recorded label = %q", sp.Label)
	}
	// An explicit label renames the work.
	if err := r.fleet.End(ctx, "bees-8"); err != nil {
		t.Fatal(err)
	}
	if st, err := r.fleet.Spawn(ctx, "bees-8", "new words"); err != nil || st.Label != "new words" {
		t.Fatalf("started = %+v, err = %v; want the new label", st, err)
	}
}

func TestCheckLabel(t *testing.T) {
	for _, ok := range []string{"role prompts", "squash merge end", "r40", "a b c d", strings.Repeat("x", 32)} {
		if err := worker.CheckLabel(ok); err != nil {
			t.Errorf("CheckLabel(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a b c d e", strings.Repeat("x", 33), "Role prompts", "role  prompts", " role", "role prompts.", `"role"`, "role-prompts", "rôle", "role\tprompts"} {
		if err := worker.CheckLabel(bad); !errors.Is(err, worker.ErrBadLabel) {
			t.Errorf("CheckLabel(%q) = %v, want ErrBadLabel", bad, err)
		}
	}
}

func TestBeadMustBelongToTheProject(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	for _, bead := range []string{"dotfiles-3", "beads-x7q"} {
		if _, err := r.fleet.Spawn(ctx, bead, "spawn and end"); !errors.Is(err, worker.ErrWrongRepo) {
			t.Errorf("%s: err = %v, want ErrWrongRepo", bead, err)
		}
	}
	r.fleet.ForeignRepo = true
	if _, err := r.fleet.Spawn(ctx, "dotfiles-3", "spawn and end"); err != nil {
		t.Fatalf("with ForeignRepo: %v", err)
	}
}

func TestEndRemovesOnlyTheRecordedWorkspace(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if err := r.fleet.End(ctx, "bees-8"); !errors.Is(err, worker.ErrNotSpawned) {
		t.Fatalf("never spawned: err = %v, want ErrNotSpawned", err)
	}
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	recorded := r.h.workspaces[0].ID
	r.h.workspaces = append(r.h.workspaces, worker.Workspace{ID: "w-other", Label: "bees-8"}) // a decoy
	for range 2 {
		if err := r.fleet.End(ctx, "bees-8"); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(r.h.removed, []string{recorded}) {
		t.Fatalf("removed = %v, want only %s", r.h.removed, recorded)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); !sp.Ended {
		t.Fatalf("spawn not marked ended: %+v", sp)
	}
}

// fakeGitHub answers MergedHeads with a fixed list or error.
type fakeGitHub struct {
	heads []string
	err   error
}

func (g fakeGitHub) MergedHeads(ctx context.Context, repo, branch string) ([]string, error) {
	return g.heads, g.err
}

// gitCommit writes files (name → content) in dir and commits them.
func gitCommit(t *testing.T, dir, msg string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, dir, "add", name)
	}
	gitRun(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", msg)
}

func offline(string) worker.GitHub { return fakeGitHub{err: errors.New("offline")} }

func gitTip(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestPruneDeletesTheBranchOnlyIfMerged(t *testing.T) {
	// Each case starts from main with a.txt and b.txt, and worker bees-8 spawned. work
	// builds branch bees-8 (and lands it, or not) and leaves main checked out.
	branch := func(t *testing.T, repo string) {
		gitRun(t, repo, "checkout", "-q", "-b", "bees-8")
		gitCommit(t, repo, "work 1", map[string]string{"a.txt": "A1"})
		gitCommit(t, repo, "work 2", map[string]string{"c.txt": "C"})
		gitRun(t, repo, "checkout", "-q", "main")
	}
	cases := []struct {
		name    string
		gh      func(tip string) worker.GitHub // nil: no GitHub
		work    func(t *testing.T, repo string)
		kept    string // substring of the reason; "" means deleted
		unknown bool   // kept because a probe failed, not because it is unmerged
	}{
		{name: "no commits of its own", work: func(t *testing.T, repo string) { gitRun(t, repo, "branch", "bees-8") }},
		{name: "merge commit", work: func(t *testing.T, repo string) {
			branch(t, repo)
			gitRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "merge", "-q", "--no-ff", "-m", "merge", "bees-8")
		}},
		{name: "unmerged", kept: "it has work not on main", work: branch},
		{name: "only empty commits", kept: "not on main", work: func(t *testing.T, repo string) {
			gitRun(t, repo, "checkout", "-q", "-b", "bees-8")
			gitCommit(t, repo, "empty", nil)
			gitRun(t, repo, "checkout", "-q", "main")
		}},
		{name: "rebase merge", work: func(t *testing.T, repo string) {
			gitCommit(t, repo, "main moves first", map[string]string{"b.txt": "B1"})
			branch(t, repo)
			gitRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "cherry-pick", "main..bees-8")
		}},
		{name: "squash merge", work: func(t *testing.T, repo string) {
			branch(t, repo)
			gitRun(t, repo, "merge", "-q", "--squash", "bees-8")
			gitCommit(t, repo, "squash", nil)
		}},
		{name: "squash merge, then main moved on elsewhere", work: func(t *testing.T, repo string) {
			branch(t, repo)
			gitRun(t, repo, "merge", "-q", "--squash", "bees-8")
			gitCommit(t, repo, "squash", nil)
			gitCommit(t, repo, "later", map[string]string{"b.txt": "B2"})
		}},
		{name: "squash merge, then main changed the branch's file", kept: "not on main", work: func(t *testing.T, repo string) {
			branch(t, repo)
			gitRun(t, repo, "merge", "-q", "--squash", "bees-8")
			gitCommit(t, repo, "squash", nil)
			gitCommit(t, repo, "later", map[string]string{"a.txt": "A2"})
		}},
		{name: "GitHub merged a PR whose head is the tip", work: func(t *testing.T, repo string) {
			branch(t, repo)
		}, gh: func(tip string) worker.GitHub { return fakeGitHub{heads: []string{tip}} }},
		{name: "GitHub merged a PR from an older tip", kept: "not on main", work: branch,
			gh: func(string) worker.GitHub {
				return fakeGitHub{heads: []string{"0123456789abcdef0123456789abcdef01234567"}}
			}},
		{name: "GitHub unreachable, content merged", gh: offline, work: func(t *testing.T, repo string) {
			branch(t, repo)
			gitRun(t, repo, "merge", "-q", "--squash", "bees-8")
			gitCommit(t, repo, "squash", nil)
		}},
		{name: "GitHub unreachable, unmerged", kept: "not on main (GitHub not checked: offline)", work: branch,
			gh: offline},
		{name: "no default branch", kept: "no default branch", unknown: true, work: func(t *testing.T, repo string) {
			branch(t, repo)
			gitRun(t, repo, "checkout", "-q", "bees-8")
			gitRun(t, repo, "branch", "-q", "-m", "main", "trunk")
			gitRun(t, repo, "checkout", "-q", "trunk")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			r := newRig(t)
			gitRun(t, "", "init", "-q", "-b", "main", r.repo)
			gitCommit(t, r.repo, "init", map[string]string{"a.txt": "A", "b.txt": "B"})
			_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
			c.work(t, r.repo)
			if c.gh != nil {
				r.fleet.G = c.gh(gitTip(t, r.repo, "bees-8"))
			}
			if err := r.fleet.End(ctx, "bees-8"); err != nil {
				t.Fatal(err)
			}
			pruned, err := r.fleet.Prune(ctx, false)
			if err != nil || len(pruned) != 1 {
				t.Fatalf("prune: %+v, %v", pruned, err)
			}
			p := pruned[0]
			exists := branchExists(r.repo, "bees-8")
			if c.kept == "" && (p.Merge != worker.Merged || !p.Deleted || exists) {
				t.Fatalf("want deleted: %+v, branch exists=%v", p, exists)
			}
			want := worker.NotMerged
			if c.unknown {
				want = worker.Unknown
			}
			if c.kept != "" && (p.Merge != want || !strings.Contains(p.Why, c.kept) || p.Deleted || !exists) {
				t.Fatalf("want kept (%v) with %q: %+v, branch exists=%v", want, c.kept, p, exists)
			}
		})
	}
}

func branchExists(repo, branch string) bool {
	return exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// squashMerged gives r a repo whose branch bees-8 is squash-merged into main.
func squashMerged(t *testing.T, r rig) {
	t.Helper()
	gitRun(t, "", "init", "-q", "-b", "main", r.repo)
	gitCommit(t, r.repo, "init", map[string]string{"a.txt": "A"})
	gitRun(t, r.repo, "checkout", "-q", "-b", "bees-8")
	gitCommit(t, r.repo, "work", map[string]string{"a.txt": "A1"})
	gitRun(t, r.repo, "checkout", "-q", "main")
	gitRun(t, r.repo, "merge", "-q", "--squash", "bees-8")
	gitCommit(t, r.repo, "squash", nil)
}

func TestEndKeepsTheBranch(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	squashMerged(t, r)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if err := r.fleet.End(ctx, "bees-8"); err != nil {
		t.Fatal(err)
	}
	if !branchExists(r.repo, "bees-8") {
		t.Fatal("bs end deleted a branch; only prune may")
	}
}

func TestPruneDryRunDeletesNothing(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	squashMerged(t, r)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	_ = r.fleet.End(ctx, "bees-8")
	pruned, err := r.fleet.Prune(ctx, true)
	if err != nil || len(pruned) != 1 || pruned[0].Merge != worker.Merged || pruned[0].Deleted {
		t.Fatalf("dry run: %+v, %v", pruned, err)
	}
	if !branchExists(r.repo, "bees-8") {
		t.Fatal("dry run deleted the branch")
	}
}

func TestPruneTouchesOnlyEndedWorkersRecordedBranches(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	squashMerged(t, r)
	gitRun(t, r.repo, "branch", "bees-9") // merged, but bs never spawned bees-9
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if pruned, err := r.fleet.Prune(ctx, false); err != nil || len(pruned) != 0 {
		t.Fatalf("live worker: %+v, %v", pruned, err)
	}
	_ = r.fleet.End(ctx, "bees-8")
	r.h.exit("bees-8")
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil { // live once more
		t.Fatal(err)
	}
	if pruned, err := r.fleet.Prune(ctx, false); err != nil || len(pruned) != 0 {
		t.Fatalf("respawned worker: %+v, %v", pruned, err)
	}
	_ = r.fleet.End(ctx, "bees-8")
	for range 2 { // the second prune finds the branch gone
		if _, err := r.fleet.Prune(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	if branchExists(r.repo, "bees-8") || !branchExists(r.repo, "bees-9") {
		t.Fatalf("bees-8 exists=%v (want deleted), bees-9 exists=%v (want kept)", branchExists(r.repo, "bees-8"), branchExists(r.repo, "bees-9"))
	}
	if pruned, err := r.fleet.Prune(ctx, false); err != nil || len(pruned) != 0 {
		t.Fatalf("after pruning: %+v, %v", pruned, err)
	}
}

func TestSpawnRecordsTheBriefWithTheSpawn(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.failStart = errors.New("crash after recording")
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	unread, err := r.lb.Unread(ctx, "bees-8")
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
