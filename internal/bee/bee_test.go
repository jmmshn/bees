package bee_test

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
	"time"

	"github.com/jmmshn/bees/internal/bee"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/project"
	"github.com/jmmshn/bees/internal/prompt"
)

// fakeHerdr keeps workspaces and live Claude sessions in memory, and records removals.
// Like herdr 0.9.3 it keeps any label verbatim, even empty or with newlines
// (TestHerdrKeepsLabelsVerbatim), so CheckLabel is bs's rule, not herdr's.
// It is also the fake Claude registry (bee.Sessions), so a start shows up there.
type fakeHerdr struct {
	next       int
	workspaces []bee.Workspace
	agents     map[string]string // live Claude sessions: name → pane
	started    [][]string        // pane, name, args...
	removed    []string
	failStart  error
	liveAnyway bool  // with failStart: Claude came up all the same, as after herdr's timeout
	aliveErr   error // the registry can't be read
}

func (h *fakeHerdr) CreateWorktree(ctx context.Context, repo, branch, path, label string) (bee.Workspace, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return bee.Workspace{}, err
	}
	h.next++
	ws := bee.Workspace{ID: fmt.Sprintf("w%d", h.next), Label: label, Pane: fmt.Sprintf("w%d:p1", h.next), Path: path}
	h.workspaces = append(h.workspaces, ws)
	return ws, nil
}

func (h *fakeHerdr) StartClaude(ctx context.Context, pane, name string, args ...string) error {
	if err := bee.CheckAgentArgs(args); err != nil { // herdr's own rule, held to real herdr
		return err
	}
	if h.failStart == nil || h.liveAnyway {
		h.started = append(h.started, append([]string{pane, name}, args...))
		h.agents[name] = pane
	}
	return h.failStart
}

func (h *fakeHerdr) Alive(ctx context.Context, name string, wait time.Duration) (bool, error) {
	_, ok := h.agents[name]
	return ok, h.aliveErr
}

func (h *fakeHerdr) RemoveWorktree(ctx context.Context, workspace string) error {
	h.removed = append(h.removed, workspace)
	h.workspaces = slices.DeleteFunc(h.workspaces, func(w bee.Workspace) bool { return w.ID == workspace })
	return nil
}

func (h *fakeHerdr) Workspaces(ctx context.Context) ([]bee.Workspace, error) {
	return h.workspaces, nil
}

// exit simulates the bee's Claude process ending.
func (h *fakeHerdr) exit(name string) { delete(h.agents, name) }

type fakeBeads map[string]bee.Bead

func (b fakeBeads) Show(ctx context.Context, id string) (bee.Bead, error) {
	bead, ok := b[id]
	if !ok {
		return bee.Bead{}, fmt.Errorf("no bead %s", id)
	}
	return bead, nil
}

type rig struct {
	fleet bee.Fleet
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
		"dotfiles-3": {ID: "dotfiles-3", Title: "dotfiles thing"},
		"beads-x7q":  {ID: "beads-x7q", Title: "flat id"},
	}
	repo := filepath.Join(dir, "bees")
	p := project.Project{Name: "bees", Repo: repo, Logbook: filepath.Join(dir, "bees.db")}
	return rig{fleet: bee.Fleet{P: p, LB: lb, H: h, S: h, B: beads, Me: "queen-bees"}, h: h, lb: lb, repo: repo}
}

func TestName(t *testing.T) {
	for bead, want := range map[string]string{"bees-8": "bees-8", "bees-12.3": "bees-12-3", "pymatgenanalysisdefects-12.3": "pymatgenanalysisdefects-12-3"} {
		if got, err := bee.Name(bead); err != nil || got != want {
			t.Errorf("Name(%q) = %q, %v; want %q", bead, got, err, want)
		}
	}
	for _, bad := range []string{"bees-1" + strings.Repeat(".1", 14), "", "Bees-1"} {
		if _, err := bee.Name(bad); err == nil {
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
		if got := bee.Belongs(c.project, c.bead); got != c.want {
			t.Errorf("Belongs(%q, %q) = %v, want %v", c.project, c.bead, got, c.want)
		}
	}
}

func TestNextEpic(t *testing.T) {
	ids := []string{"bees-1", "bees-1.4", "bees-9", "bees-10.2", "dotfiles-40", "beads-995"}
	if got := bee.NextEpic("bees", ids); got != "bees-11" {
		t.Fatalf("NextEpic = %q, want bees-11", got)
	}
	if got := bee.NextEpic("crew", ids); got != "crew-1" {
		t.Fatalf("NextEpic(empty project) = %q, want crew-1", got)
	}
}

func TestSpawnStartsABeeInItsOwnWorkspace(t *testing.T) {
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
	var settings map[string]any
	if err := json.Unmarshal([]byte(start[i+1]), &settings); err != nil || settings["crossSessionInbound"] != "accept" {
		t.Fatalf("--settings %q: %v", start[i+1], err)
	}
}

func TestSecondSpawnWhileLiveIsRefused(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); !errors.Is(err, bee.ErrRunning) {
		t.Fatalf("err = %v, want ErrRunning", err)
	}
	if len(r.h.workspaces) != 1 {
		t.Fatalf("workspaces = %d, want 1", len(r.h.workspaces))
	}
}

func TestSpawnResumesADeadBeeInItsRecordedWorkspace(t *testing.T) {
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

func TestResumeContinuesTheBeesConversation(t *testing.T) {
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

func TestRestartOfABeeThatNeverReadItsBriefStartsFresh(t *testing.T) {
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

func TestEndedHalfStartedBeeRespawnsWithOneBrief(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.failStart = errors.New("herdr agent: agent arguments cannot be encoded safely")
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if _, err := r.fleet.End(ctx, "bees-8"); err != nil {
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

func TestRestartInAnotherProjectsRepoContinues(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.fleet.ForeignRepo, r.fleet.P.Repo = true, filepath.Join(t.TempDir(), "dotfiles")
	_, _ = r.fleet.Spawn(ctx, "dotfiles-3", "spawn and end")
	r.h.exit("dotfiles-3")
	// Its bs resolves the other project, so its receipts are in that logbook: this one
	// can't tell whether it has a conversation, and keeps the old behaviour.
	if st, err := r.fleet.Spawn(ctx, "dotfiles-3", "spawn and end"); err != nil || !st.Continued || !slices.Contains(r.h.started[1], "--continue") {
		t.Fatalf("started = %+v, err = %v, args = %q; want --continue", st, err, r.h.started[1])
	}
}

// readInbox does what the bee's bs inbox does: records receipts for its unread messages.
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

func TestBeesStartWithTheProjectsSystemPrompt(t *testing.T) {
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

func TestABeeForAnotherProjectsBeadGetsNoSystemPrompt(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	own := r.fleet
	r.fleet.ForeignRepo, r.fleet.P.Repo = true, filepath.Join(t.TempDir(), "dotfiles") // as --repo does
	_, _ = r.fleet.Spawn(ctx, "dotfiles-3", "spawn and end")
	r.h.exit("dotfiles-3")
	_, _ = own.Spawn(ctx, "dotfiles-3", "spawn and end") // a resume doesn't repeat --repo
	for _, start := range r.h.started {
		if slices.Contains(start, "--append-system-prompt-file") {
			t.Fatalf("start = %q: a system prompt naming bees would mislead it: the bee's bs resolves its own repo's project", start)
		}
	}
}

func TestBeeWaitingAtAPromptCountsAsStarted(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.failStart = fmt.Errorf("herdr agent start: blocked during startup: %w", bee.ErrWaiting)
	st, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if err != nil || !st.Waiting || st.Name != "bees-8" {
		t.Fatalf("started = %+v, err = %v; want waiting, no error", st, err)
	}
}

func TestHerdrTimeoutWithClaudeRunningCountsAsStarted(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	// herdr stops waiting while Claude is busy with its first prompt (bees-1.14).
	r.h.failStart, r.h.liveAnyway = fmt.Errorf("herdr agent: timed out waiting for agent startup: %w", bee.ErrUnconfirmed), true
	st, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if err != nil || st.Name != "bees-8" || st.Waiting {
		t.Fatalf("started = %+v, err = %v; want started", st, err)
	}
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); !errors.Is(err, bee.ErrRunning) {
		t.Fatalf("second spawn: err = %v, want ErrRunning (herdr never named it, but Claude runs)", err)
	}
}

func TestHerdrTimeoutWithNoClaudeIsAFailure(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.failStart = fmt.Errorf("herdr agent: timed out waiting for agent startup: %w", bee.ErrUnconfirmed)
	_, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if !errors.Is(err, bee.ErrUnconfirmed) || !strings.Contains(err.Error(), "herdr pane read w1:p1") {
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

func TestUnrecordedWorkspaceOnTheBeesWorktreeIsAnOrphan(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	// Found by its worktree whatever its label: a label is free text, not an identity.
	r.h.workspaces = []bee.Workspace{{ID: "w41", Label: "bees-8", Path: r.repo + "-WT/bees-9"}, {ID: "w42", Label: "something else", Path: r.repo + "-WT/bees-8/"}}
	_, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if !errors.Is(err, bee.ErrOrphan) || !strings.Contains(err.Error(), "herdr worktree remove --workspace w42 --force") {
		t.Fatalf("err = %v, want ErrOrphan with the cleanup command", err)
	}
	if len(r.h.removed) != 0 || len(r.h.workspaces) != 2 {
		t.Fatalf("removed=%v workspaces=%v; orphans must be left alone", r.h.removed, r.h.workspaces)
	}
}

func TestFirstSpawnNeedsALabel(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", ""); !errors.Is(err, bee.ErrNoLabel) {
		t.Fatalf("err = %v, want ErrNoLabel", err)
	}
	if _, err := r.fleet.Spawn(ctx, "bees-8", "Spawn-And-End."); !errors.Is(err, bee.ErrBadLabel) {
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
	if _, err := r.fleet.Spawn(ctx, "bees-8", "other words"); !errors.Is(err, bee.ErrLabelChange) || len(r.h.started) != 1 {
		t.Fatalf("err = %v, starts = %d; want ErrLabelChange and no start", err, len(r.h.started))
	}
	st, err := r.fleet.Spawn(ctx, "bees-8", "")
	if err != nil || !st.Resumed || st.Label != "spawn and end" {
		t.Fatalf("started = %+v, err = %v; want a resume with the recorded label", st, err)
	}
	if _, err := r.fleet.Spawn(ctx, "bees-8", ""); !errors.Is(err, bee.ErrRunning) {
		t.Fatalf("live bee: err = %v, want ErrRunning", err)
	}
}

func TestCheckLabel(t *testing.T) {
	for _, ok := range []string{"role prompts", "squash merge end", "r40", "a b c d", strings.Repeat("x", 32)} {
		if err := bee.CheckLabel(ok); err != nil {
			t.Errorf("CheckLabel(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a b c d e", strings.Repeat("x", 33), "Role prompts", "role  prompts", " role", "role prompts.", `"role"`, "role-prompts", "rôle", "role\tprompts"} {
		if err := bee.CheckLabel(bad); !errors.Is(err, bee.ErrBadLabel) {
			t.Errorf("CheckLabel(%q) = %v, want ErrBadLabel", bad, err)
		}
	}
}

func TestBeadMustBelongToTheProject(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	for _, bead := range []string{"dotfiles-3", "beads-x7q"} {
		if _, err := r.fleet.Spawn(ctx, bead, "spawn and end"); !errors.Is(err, bee.ErrWrongRepo) {
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
	if _, err := r.fleet.End(ctx, "bees-8"); !errors.Is(err, bee.ErrNotSpawned) {
		t.Fatalf("never spawned: err = %v, want ErrNotSpawned", err)
	}
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	recorded := r.h.workspaces[0].ID
	r.h.workspaces = append(r.h.workspaces, bee.Workspace{ID: "w-other", Label: "bees-8"}) // a decoy
	for range 2 {
		if _, err := r.fleet.End(ctx, "bees-8"); err != nil {
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

func TestEndDeletesTheBranchOnlyIfMerged(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	gitRun(t, "", "init", "-q", "-b", "main", r.repo)
	gitRun(t, r.repo, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "init")
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	gitRun(t, r.repo, "branch", "bees-8") // merged: no commits of its own
	if kept, err := r.fleet.End(ctx, "bees-8"); err != nil || kept {
		t.Fatalf("merged branch: kept=%v err=%v", kept, err)
	}
	r.h.exit("bees-8")
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	gitRun(t, r.repo, "checkout", "-q", "-b", "bees-8")
	gitRun(t, r.repo, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "work")
	gitRun(t, r.repo, "checkout", "-q", "main")
	if kept, err := r.fleet.End(ctx, "bees-8"); err != nil || !kept {
		t.Fatalf("unmerged branch: kept=%v err=%v", kept, err)
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
