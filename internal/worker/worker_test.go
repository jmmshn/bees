package worker_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/harness/claude"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
	"github.com/jmmshn/bees/internal/project"
	"github.com/jmmshn/bees/internal/prompt"
	"github.com/jmmshn/bees/internal/worker"
)

// fakeHerdr keeps workspaces and live Claude sessions in memory, and records removals.
// Like herdr 0.9.3 it keeps any label verbatim, even empty or with newlines
// (TestHerdrKeepsLabelsVerbatim), so CheckLabel is bs's rule, not herdr's.
// It is also the process table (worker.Procs): a start that comes up runs a process named
// for its kind under the pane's shell, as Claude does, and exit ends it.
type fakeHerdr struct {
	mu          sync.Mutex // SpawnAll starts workers concurrently
	together    int        // >0: StartAgent waits until this many starts are in flight, else fails
	kind        string     // the last start's herdr kind
	inFlight    int
	next        int
	workspaces  []worker.Workspace
	agents      map[string]string // live Claude sessions: name → pane
	started     [][]string        // pane, name, args...
	cuts        [][2]string       // the repo and base ref each worktree was cut from
	removed     []string
	closed      []string
	closeErr    error           // herdr can't close a workspace
	tagged      []string        // the path each plain workspace was tagged with
	tagErr      error           // herdr can't tag a workspace
	dirty       map[string]bool // worktree paths with modified or untracked files
	failStart   error
	busyStarts  int   // the next starts herdr refuses as agent_pane_busy, typing nothing
	liveAnyway  bool  // with failStart: Claude came up all the same, as after herdr's timeout
	aliveErr    error // the process table can't be read
	walkErr     error // the descendants can't be read
	portsErr    error // the listening ports can't be read
	listErr     error // herdr can't be reached
	procs       map[int]*fakeProc
	pids        map[string]int // agent name → its process
	nextPID     int
	renamed     []string       // pane=name, one per RenameAgent
	helpers     []string       // processes each harness starts with, so its baseline holds them
	moved       map[string]int // a moved pane's id → the shell it kept
	closedPanes []string
}

// fakeProc is a process: a pid with a start time, a name and a parent, alive until it exits.
type fakeProc struct {
	name, start string
	parent      int
	alive       bool
	ports       []int
}

// spawnProc runs a process named name under parent and returns its pid. Callers hold mu.
func (h *fakeHerdr) spawnProc(name string, parent int) int {
	if h.procs == nil {
		h.procs, h.pids, h.nextPID = map[int]*fakeProc{}, map[string]int{}, 1000
	}
	h.nextPID++
	h.procs[h.nextPID] = &fakeProc{name: name, start: fmt.Sprintf("t%d", h.nextPID), parent: parent, alive: true}
	return h.nextPID
}

// shellOf is pane's shell: the one it kept if it was moved, else shell(pane). Callers hold mu.
func (h *fakeHerdr) shellOf(pane string) int {
	if s, ok := h.moved[pane]; ok {
		return s
	}
	return shell(pane)
}

// shell is pane's shell pid: one per pane, fixed by its name.
func shell(pane string) int {
	n := 0
	for _, c := range pane {
		n = n*31 + int(c)
	}
	return 1 + n%900
}

func (h *fakeHerdr) CreateWorktree(ctx context.Context, repo, base, branch, path, label string) (worker.Workspace, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Like herdr 0.9.5: "New and open worktree actions start from the repo parent workspace".
	if fi, err := os.Stat(filepath.Join(repo, ".git")); err == nil && !fi.IsDir() {
		return worker.Workspace{}, fmt.Errorf("herdr worktree: %s is a linked worktree", repo)
	}
	h.cuts = append(h.cuts, [2]string{repo, base})
	if err := os.MkdirAll(path, 0o755); err != nil {
		return worker.Workspace{}, err
	}
	h.next++
	ws := worker.Workspace{ID: fmt.Sprintf("w%d", h.next), Label: label, Pane: fmt.Sprintf("w%d:p1", h.next), Path: path}
	h.workspaces = append(h.workspaces, ws)
	return ws, nil
}

func (h *fakeHerdr) StartAgent(ctx context.Context, pane, name, kind string, args ...string) error {
	if err := worker.CheckAgentArgs(args); err != nil { // herdr's own rule, held to real herdr
		return err
	}
	if err := h.waitTogether(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.busyStarts > 0 { // real herdr refuses before typing: nothing starts
		h.busyStarts--
		return fmt.Errorf("herdr agent: agent target pane %s is not an available shell: %w", pane, worker.ErrPaneBusy)
	}
	if h.failStart == nil || h.liveAnyway {
		h.started = append(h.started, append([]string{pane, name}, args...))
		h.agents[name] = pane
		h.kind = kind
	}
	// The harness process runs whenever the start came up, at a prompt too.
	if h.failStart == nil || h.liveAnyway || errors.Is(h.failStart, worker.ErrWaiting) {
		h.pids[name] = h.spawnProc(kind, shell(pane))
		h.agents[name] = pane
		for _, c := range h.helpers {
			h.spawnProc(c, h.pids[name])
		}
	}
	return h.failStart
}

// ProcessInfo shows the live harness process in pane in the foreground; a pane of no open
// workspace is pane_not_found.
func (h *fakeHerdr) ProcessInfo(ctx context.Context, pane string) (worker.PaneProcesses, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.listErr != nil {
		return worker.PaneProcesses{}, h.listErr
	}
	ws, _, _ := strings.Cut(pane, ":")
	_, moved := h.moved[pane]
	if !slices.ContainsFunc(h.workspaces, func(w worker.Workspace) bool { return w.ID == ws && (w.Pane == pane || moved) }) {
		return worker.PaneProcesses{}, fmt.Errorf("herdr pane: pane not found: %w", worker.ErrPaneGone)
	}
	pi := worker.PaneProcesses{Shell: h.shellOf(pane)}
	for name, p := range h.agents {
		if pr := h.procs[h.pids[name]]; p == pane && pr != nil && pr.alive {
			pi.Foreground = append(pi.Foreground, worker.PaneProcess{PID: h.pids[name], Name: pr.name})
		}
	}
	return pi, nil
}

// ClosePane ends the agent in pane, as closing a herdr pane does.
func (h *fakeHerdr) ClosePane(ctx context.Context, pane string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closedPanes = append(h.closedPanes, pane)
	for name, p := range h.agents {
		if p == pane {
			if pr := h.procs[h.pids[name]]; pr != nil {
				pr.alive = false
			}
			delete(h.agents, name)
		}
	}
	return nil
}

func (h *fakeHerdr) RenameAgent(ctx context.Context, pane, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.renamed = append(h.renamed, pane+"="+name)
	return nil
}

func (h *fakeHerdr) Alive(p proc.Process) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pr := h.procs[p.PID]
	return pr != nil && pr.alive && pr.start == p.Start, h.aliveErr
}

func (h *fakeHerdr) Start(pid int) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if pr := h.procs[pid]; pr != nil && pr.alive {
		return pr.start, nil
	}
	return "", proc.ErrGone
}

func (h *fakeHerdr) Name(pid int) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if pr := h.procs[pid]; pr != nil && pr.alive {
		return pr.name, nil
	}
	return "", proc.ErrGone
}

func (h *fakeHerdr) Exe(pid int) (string, error) { return "/fake/" + fmt.Sprint(pid), nil }

// Descendants is every live process below pid, level by level, as proc's is.
func (h *fakeHerdr) Descendants(pid int) ([]proc.Process, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.walkErr != nil {
		return nil, h.walkErr
	}
	var out []proc.Process
	for level := []int{pid}; len(level) > 0; {
		var next []int
		for _, n := range slices.Sorted(maps.Keys(h.procs)) {
			if pr := h.procs[n]; pr.alive && slices.Contains(level, pr.parent) {
				out = append(out, proc.Process{PID: n, Start: pr.start})
				next = append(next, n)
			}
		}
		level = next
	}
	return out, nil
}

func (h *fakeHerdr) Parent(pid int) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if pr := h.procs[pid]; pr != nil && pr.alive {
		return pr.parent, nil
	}
	return 0, proc.ErrGone
}

func (h *fakeHerdr) Command(pid int) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if pr := h.procs[pid]; pr != nil && pr.alive {
		return pr.name, nil
	}
	return "", proc.ErrGone
}

// Listening is each pid's ports, set on its fakeProc.
func (h *fakeHerdr) Listening(ctx context.Context, pids []int) (map[int][]int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.portsErr != nil {
		return nil, h.portsErr
	}
	out := map[int][]int{}
	for _, p := range pids {
		if pr := h.procs[p]; pr != nil && pr.alive && len(pr.ports) > 0 {
			out[p] = pr.ports
		}
	}
	return out, nil
}

// movePane moves worker name's pane into another workspace, as herdr pane move does: the
// pane gets a new id, the emptied workspace closes, and the process runs on.
func (h *fakeHerdr) movePane(name string) {
	h.next++
	h.movePaneInto(name, fmt.Sprintf("w%d", h.next))
}

// movePaneInto is movePane into workspace ws.
func (h *fakeHerdr) movePaneInto(name, ws string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.agents[name]
	h.next++ // herdr never reuses a pane id
	pane := fmt.Sprintf("%s:p%d", ws, h.next)
	h.agents[name] = pane
	if h.moved == nil {
		h.moved = map[string]int{}
	}
	h.moved[pane] = h.shellOf(old) // the pane keeps its shell, under its new id
	h.workspaces = slices.DeleteFunc(h.workspaces, func(w worker.Workspace) bool { return w.Pane == old })
}

// Panes lists ws's root pane and any agent's pane moved into it.
func (h *fakeHerdr) Panes(ctx context.Context, ws string) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.listErr != nil {
		return nil, h.listErr
	}
	i := slices.IndexFunc(h.workspaces, func(w worker.Workspace) bool { return w.ID == ws })
	if i < 0 {
		return nil, nil
	}
	panes := []string{h.workspaces[i].Pane}
	for _, p := range h.agents {
		if strings.HasPrefix(p, ws+":") && !slices.Contains(panes, p) {
			panes = append(panes, p)
		}
	}
	return panes, nil
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

// RemoveWorktree refuses a dirty worktree without force, as herdr 0.9.3 does with
// dirty_worktree_requires_force (TestHerdrDirtyWorktreeNeedsForce).
func (h *fakeHerdr) RemoveWorktree(ctx context.Context, workspace string, force bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i := slices.IndexFunc(h.workspaces, func(w worker.Workspace) bool { return w.ID == workspace }); i >= 0 && h.dirty[h.workspaces[i].Path] && !force {
		return fmt.Errorf("herdr worktree: contains modified or untracked files, use --force: %w", worker.ErrDirty)
	}
	h.removed = append(h.removed, workspace)
	h.dropWorkspaces(func(w worker.Workspace) bool { return w.ID == workspace })
	return nil
}

// CreateWorkspace opens on $HOME for a dir that doesn't exist, as herdr 0.9.5 does
// (exit 0), and lists no path or worker until the workspace is tagged.
func (h *fakeHerdr) CreateWorkspace(ctx context.Context, dir, label string) (worker.Workspace, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := os.Stat(dir); err != nil {
		dir = os.Getenv("HOME")
	}
	h.next++
	ws := worker.Workspace{ID: fmt.Sprintf("w%d", h.next), Label: label, Pane: fmt.Sprintf("w%d:p1", h.next)}
	h.workspaces = append(h.workspaces, ws)
	ws.Path = dir
	return ws, nil
}

// TagWorkspace cuts each value to 80 characters, as herdr 0.9.5 does without a word; the
// path token reads back as no Path, as HerdrCLI.Workspaces lists it.
func (h *fakeHerdr) TagWorkspace(ctx context.Context, workspace, name, path string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := slices.IndexFunc(h.workspaces, func(w worker.Workspace) bool { return w.ID == workspace })
	if i < 0 {
		return fmt.Errorf("herdr workspace report-metadata: workspace %s not found", workspace)
	}
	if h.tagErr != nil {
		return h.tagErr
	}
	h.workspaces[i].Worker = name[:min(len(name), 80)]
	h.tagged = append(h.tagged, path)
	return nil
}

func (h *fakeHerdr) CloseWorkspace(ctx context.Context, workspace string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closeErr != nil {
		return h.closeErr
	}
	h.closed = append(h.closed, workspace)
	h.dropWorkspaces(func(w worker.Workspace) bool { return w.ID == workspace })
	return nil
}

func (h *fakeHerdr) Workspaces(ctx context.Context) ([]worker.Workspace, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.workspaces, h.listErr
}

// exit simulates the worker's Claude process ending.
// closeWorkspaces closes the workspaces drop picks, and, as herdr does, ends the
// processes in their panes.
func (h *fakeHerdr) closeWorkspaces(drop func(worker.Workspace) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropWorkspaces(drop)
}

// dropWorkspaces is closeWorkspaces for a caller holding mu.
func (h *fakeHerdr) dropWorkspaces(drop func(worker.Workspace) bool) {
	for _, w := range h.workspaces {
		if !drop(w) {
			continue
		}
		for name, pane := range h.agents {
			if pane == w.Pane {
				if pr := h.procs[h.pids[name]]; pr != nil {
					pr.alive = false
				}
				delete(h.agents, name)
			}
		}
	}
	h.workspaces = slices.DeleteFunc(h.workspaces, drop)
}

func (h *fakeHerdr) exit(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if pr := h.procs[h.pids[name]]; pr != nil {
		pr.alive = false
	}
	delete(h.agents, name)
}

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
	t.Setenv("HOME", dir) // macOS ignores XDG_CACHE_HOME: keep prompt and settings files out of ~/Library/Caches
	lb, err := logbook.Open(ctx, filepath.Join(dir, "bees.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lb.Close() })
	h := &fakeHerdr{agents: map[string]string{}, procs: map[int]*fakeProc{}, pids: map[string]int{}, nextPID: 1000}
	beads := fakeBeads{
		"bees-8":     {ID: "bees-8", Title: "spawn and end bees", Description: "Build bs spawn."},
		"bees-8.1":   {ID: "bees-8.1", Title: "child of bees-8"},
		"bees-9":     {ID: "bees-9", Title: "second worker"},
		"bees-10":    {ID: "bees-10", Title: "third worker"},
		"dotfiles-3": {ID: "dotfiles-3", Title: "dotfiles thing"},
		"beads-x7q":  {ID: "beads-x7q", Title: "flat id"},
	}
	repo := filepath.Join(dir, "bees")
	gitInit(t, repo) // the guards read its default branch
	p := project.Project{Name: "bees", Repo: repo, Worktrees: repo + "-WT", Logbook: filepath.Join(dir, "bees.db")}
	return rig{fleet: worker.Fleet{P: p, LB: lb, H: h, Procs: h, B: beads, Me: "queen-bees", Harness: claude.New(config.ClaudeCode{})}, h: h, lb: lb, repo: repo}
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

func TestIsWorkerOf(t *testing.T) {
	for _, c := range []struct {
		project, name string
		want          bool
	}{
		{"bees", "bees-8", true}, {"bees", "bees-8-10", true},
		{"bees", "bees-review", false}, {"bees", "bees-8-", false}, {"bees", "queen-bees", false},
		{"bees", "bees", false}, {"bees", "beesx-1", false}, {"bees", "keeper", false},
	} {
		if got := worker.IsWorkerOf(c.project, c.name); got != c.want {
			t.Errorf("IsWorkerOf(%q, %q) = %v, want %v", c.project, c.name, got, c.want)
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
	if ws.Path != wt || ws.Label != "bees · spawn and end" {
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
	if err := json.Unmarshal(settingsFile(t, start), &settings); err != nil || settings.CrossSessionInbound != "accept" {
		t.Fatalf("--settings %q: %v", start[i+1], err)
	}
	// A list without $defaults would drop the built-in rules (force push, exfiltration).
	for _, k := range []string{"environment", "allow"} {
		l := settings.AutoMode[k]
		if !slices.Contains(l, "$defaults") || !strings.Contains(strings.Join(l, " "), "bees-8") {
			t.Errorf("autoMode.%s = %q, want $defaults and the worker's branch", k, l)
		}
	}
	if !strings.Contains(strings.Join(settings.AutoMode["environment"], " "), wt) {
		t.Errorf("autoMode.environment = %q, want the worktree %s", settings.AutoMode["environment"], wt)
	}
	if env := strings.Join(settings.AutoMode["environment"], " "); strings.Contains(env, "merge") {
		t.Errorf("autoMode.environment = %q: without GitHub's word that main is protected, it must not claim the keeper merges", env)
	}
	denied := func(cmd string) bool {
		return slices.ContainsFunc(settings.Permissions.Deny, func(rule string) bool { return claudeRuleMatches(rule, cmd) })
	}
	for _, cmd := range []string{
		"git push --force origin bees-8", "git push origin main", "git push -u origin bees-8:main",
		"git -C /wt push origin +bees-8", "git push origin HEAD:refs/heads/main", "git -c k=v push origin main",
		"/usr/bin/git push origin main", "command git push origin master", "command /usr/bin/git -C /wt push -f",
		"gh pr merge 41", "gh pr review 41 --approve",
	} {
		if !denied(cmd) {
			t.Errorf("deny rules let through %q", cmd)
		}
	}
	for _, cmd := range []string{
		"git push -u origin bees-8", "git -C /wt push origin bees-8", "git push origin HEAD:refs/heads/bees-8",
		"git commit -m 'deny git push to main'", "gh pr create --base main",
	} {
		if denied(cmd) {
			t.Errorf("deny rules refuse the worker's own %q", cmd)
		}
	}
}

// claudeRuleMatches is Claude Code's Bash permission rule match: Bash(<pattern>) where *
// matches any text, spaces and slashes too (probed on Claude Code 2.1.302 with
// claude -p --settings: Bash(*/git push*) denied /usr/bin/git push origin main).
func claudeRuleMatches(rule, cmd string) bool {
	pat, ok := strings.CutPrefix(rule, "Bash(")
	if !ok {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(pat, ")"), "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$").MatchString(cmd)
}

// The environment tells the classifier the keeper merges by hand only when GitHub says
// the default branch is protected (bees-7.4).
func TestWorkerEnvironmentClaimsReviewOnlyOnAProtectedMain(t *testing.T) {
	for i, gh := range []worker.GitHub{nil, fakeGitHub{protected: true, err: errors.New("offline")}, fakeGitHub{protected: true}} {
		r := newRig(t)
		r.fleet.G = gh
		if _, err := r.fleet.Spawn(context.Background(), "bees-8", "spawn and end"); err != nil {
			t.Fatal(err)
		}
		start := r.h.started[0]
		var settings struct{ AutoMode map[string][]string }
		if err := json.Unmarshal(settingsFile(t, start), &settings); err != nil {
			t.Fatal(err)
		}
		want := i == 2 // only GitHub answering "protected"
		if got := strings.Contains(strings.Join(settings.AutoMode["environment"], " "), "the keeper merges"); got != want {
			t.Errorf("GitHub %+v: environment = %q, claims keeper merges = %v, want %v", gh, settings.AutoMode["environment"], got, want)
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
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
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
	gitInit(t, r.fleet.P.Repo)
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
		if b, err := os.ReadFile(start[i+1]); err != nil || string(b) != workerPrompt(t) {
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
	gitInit(t, r.fleet.P.Repo)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.exit("bees-8")
	_, _ = own.Spawn(ctx, "bees-8", "spawn and end") // a resume doesn't repeat --repo
	if len(r.h.started) != 2 {
		t.Fatalf("started = %q, want a start and a restart", r.h.started)
	}
	// Its bs finds the spawner's project from its pane, which the spawner's logbook
	// records (ADR-0009 amendment 1); no BS_PROJECT is pinned.
	if sp, ok, _ := r.lb.LatestSpawn(ctx, "bees-8"); !ok || sp.Pane != r.h.started[1][0] {
		t.Errorf("spawn in the spawner's logbook = %+v, %v; want its pane %s", sp, ok, r.h.started[1][0])
	}
	for _, start := range r.h.started {
		i := slices.Index(start, "--append-system-prompt-file")
		if i < 0 {
			t.Fatalf("start = %q, want the bees worker prompt", start)
		}
		if b, err := os.ReadFile(start[i+1]); err != nil || string(b) != workerPrompt(t) {
			t.Fatalf("prompt file %s: %v\n%s", start[i+1], err, b)
		}
	}
}

// settingsFile returns the JSON in the file a start passes with --settings. It is a file,
// not inline JSON, so the launch line stays short enough to type (bees-8.3).
func settingsFile(t *testing.T, start []string) []byte {
	t.Helper()
	i := slices.Index(start, "--settings")
	if i < 0 {
		t.Fatalf("start = %q, want --settings", start)
	}
	b, err := os.ReadFile(start[i+1])
	if err != nil {
		t.Fatalf("--settings %q: %v", start[i+1], err)
	}
	return b
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
	defer worker.Fast(50 * time.Millisecond)()
	r.h.failStart = fmt.Errorf("herdr agent: timed out waiting for agent startup: %w", worker.ErrUnconfirmed)
	sp, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if !errors.Is(err, worker.ErrUnconfirmed) || !strings.Contains(err.Error(), "herdr pane read w1:p1") {
		t.Fatalf("err = %v, want ErrUnconfirmed pointing at the pane", err)
	}
	rec, _, _ := r.lb.LatestSpawn(ctx, sp.Name)
	if _, ok, err := r.lb.LatestStart(ctx, rec.ID); ok || err != nil {
		t.Fatalf("an unconfirmed start recorded a start: %v, %v", ok, err)
	}
}

// bees-12.33: a new pane's shell runs prompt helpers for a moment, and herdr refuses a
// start then without typing anything; the start tries again and starts exactly one.
func TestABusyNewPaneIsRetriedUntilItsShellIsFree(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	defer worker.Fast(time.Second)()
	r.h.busyStarts = 3
	if st, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil || st.Waiting {
		t.Fatalf("started = %+v, err = %v; want started", st, err)
	}
	if len(r.h.started) != 1 {
		t.Fatalf("starts = %d, want exactly one", len(r.h.started))
	}
}

// A pane that stays busy (the keeper runs something in it) fails after the bound, saying so.
func TestAPaneThatStaysBusyFailsAfterTheBound(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	defer worker.Fast(20 * time.Millisecond)()
	r.h.busyStarts = 1 << 30
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); !errors.Is(err, worker.ErrPaneBusy) || len(r.h.started) != 0 {
		t.Fatalf("err = %v, starts = %d; want ErrPaneBusy and no start", err, len(r.h.started))
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
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
		t.Fatal(err)
	}
	st, err := r.fleet.Spawn(ctx, "bees-8", "")
	if err != nil || st.Resumed || st.Label != "spawn and end" {
		t.Fatalf("started = %+v, err = %v; want a fresh spawn with the recorded label", st, err)
	}
	if ws := r.h.workspaces[len(r.h.workspaces)-1]; ws.Label != "bees · spawn and end" {
		t.Fatalf("workspace label = %q, want the recorded one", ws.Label)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); sp.Label != "spawn and end" {
		t.Fatalf("recorded label = %q", sp.Label)
	}
	// An explicit label renames the work.
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
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
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); !errors.Is(err, worker.ErrNotSpawned) {
		t.Fatalf("never spawned: err = %v, want ErrNotSpawned", err)
	}
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	recorded := r.h.workspaces[0].ID
	r.h.workspaces = append(r.h.workspaces, worker.Workspace{ID: "w-other", Label: "bees-8"}) // a decoy
	for range 2 {
		if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
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

// A workspace id herdr has handed to another worktree is not the worker's to remove (bees-7.4).
func TestEndRefusesARecordedWorkspaceOpenOnAnotherWorktree(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.workspaces[0].Path = filepath.Join(t.TempDir(), "someone-else")
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{Force: true}); !errors.Is(err, worker.ErrNotOurs) {
		t.Fatalf("err = %v, want ErrNotOurs", err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); len(r.h.removed) != 0 || sp.Ended {
		t.Fatalf("removed = %v, ended = %v; want nothing touched", r.h.removed, sp.Ended)
	}
}

// A recorded workspace herdr no longer has is already gone: the end is recorded.
func TestEndOfAVanishedWorkspaceRecordsTheEnd(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.closeWorkspaces(func(worker.Workspace) bool { return true })
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
		t.Fatal(err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); len(r.h.removed) != 0 || !sp.Ended {
		t.Fatalf("removed = %v, ended = %v; want ended, nothing removed", r.h.removed, sp.Ended)
	}
}

// bs end keeps uncommitted work unless told to discard it (bees-7.4).
func TestEndKeepsADirtyWorktreeWithoutForce(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.dirty = map[string]bool{r.h.workspaces[0].Path: true}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); !errors.Is(err, worker.ErrDirty) || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want ErrDirty naming --force", err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); len(r.h.removed) != 0 || sp.Ended {
		t.Fatalf("removed = %v, ended = %v; want the worktree kept and the worker live", r.h.removed, sp.Ended)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{Force: true}); err != nil {
		t.Fatal(err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); len(r.h.removed) != 1 || !sp.Ended {
		t.Fatalf("with force: removed = %v, ended = %v", r.h.removed, sp.Ended)
	}
}

// EndAll ends the logbook's live workers and nothing else, including one --repo spawned for
// another project's bead, keeps one it can't end, and heals on a second run (bees-8.10).
func TestEndAllEndsOnlyRecordedLiveWorkersAndHealsOnRerun(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	for _, b := range []string{"bees-8", "bees-9"} {
		if _, err := r.fleet.Spawn(ctx, b, "spawn and end"); err != nil {
			t.Fatal(err)
		}
	}
	_ = r.fleet.End(ctx, "bees-9", worker.EndOpts{})
	_, _ = r.fleet.Spawn(ctx, "bees-9", "spawn and end")  // live again, in w3
	_, _ = r.fleet.Spawn(ctx, "bees-10", "spawn and end") // w4
	r.fleet.ForeignRepo = true
	_, _ = r.fleet.Spawn(ctx, "dotfiles-3", "spawn and end")                                                                // w5: --repo, another project's bead
	r.h.workspaces = append(r.h.workspaces, worker.Workspace{ID: "w-other", Label: "bees-8", Path: r.h.workspaces[0].Path}) // not recorded
	r.h.closeWorkspaces(func(w worker.Workspace) bool { return w.ID == "w4" })                                              // bees-10's vanished
	r.h.dirty = map[string]bool{filepath.Join(r.repo+"-WT", "bees-9"): true}
	r.h.removed = nil

	summary := func(es []worker.Ending, err error) string {
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, e := range es {
			s = append(s, fmt.Sprintf("%s:%s:gone=%v:kept=%v", e.Name, e.Workspace, e.Gone, e.Err != nil))
		}
		return strings.Join(s, " ")
	}
	got, err := r.fleet.EndAll(ctx, worker.EndOpts{})
	if want := "bees-10:w4:gone=true:kept=false bees-8:w1:gone=false:kept=false bees-9:w3:gone=false:kept=true dotfiles-3:w5:gone=false:kept=false"; summary(got, err) != want {
		t.Fatalf("EndAll = %s\nwant     %s", summary(got, err), want)
	}
	if !errors.Is(got[2].Err, worker.ErrDirty) || !slices.Equal(r.h.removed, []string{"w1", "w5"}) {
		t.Fatalf("err = %v; removed = %v; want ErrDirty and only w1, w5", got[2].Err, r.h.removed)
	}
	r.h.dirty = nil
	if got := summary(r.fleet.EndAll(ctx, worker.EndOpts{})); got != "bees-9:w3:gone=false:kept=false" {
		t.Fatalf("rerun = %s; want only bees-9, now ended", got)
	}
	if slices.Contains(r.h.removed, "w-other") {
		t.Fatalf("removed a workspace that isn't the project's: %v", r.h.removed)
	}
}

// A workspace id herdr handed to another worktree is a dead end without Forget; with it the
// end is recorded and the workspace left open.
func TestEndAllForgetsAReusedWorkspace(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.workspaces[0].Path = filepath.Join(t.TempDir(), "someone-else")
	if got, _ := r.fleet.EndAll(ctx, worker.EndOpts{Force: true}); len(got) != 1 || !errors.Is(got[0].Err, worker.ErrNotOurs) {
		t.Fatalf("without Forget: %+v, want ErrNotOurs", got)
	}
	got, err := r.fleet.EndAll(ctx, worker.EndOpts{Forget: true})
	if err != nil || len(got) != 1 || got[0].Err != nil || !got[0].Forgot {
		t.Fatalf("with Forget: %+v, %v; want forgotten", got, err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); !sp.Ended || len(r.h.removed) != 0 {
		t.Fatalf("ended = %v, removed = %v; want ended, nothing removed", sp.Ended, r.h.removed)
	}
}

// With herdr down, bs can't tell open from gone: it keeps the worker live and says so.
func TestEndWithHerdrDownKeepsTheWorker(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.listErr = fmt.Errorf("%w: herdr workspace list: no herdr server is running", worker.ErrHerdrDown) // as HerdrCLI labels it
	got, err := r.fleet.EndAll(ctx, worker.EndOpts{})
	if err != nil || len(got) != 1 || !errors.Is(got[0].Err, worker.ErrHerdrDown) || got[0].Gone {
		t.Fatalf("EndAll = %+v, %v; want bees-8 kept with ErrHerdrDown", got, err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); sp.Ended || len(r.h.removed) != 0 {
		t.Fatalf("ended = %v, removed = %v; want nothing touched", sp.Ended, r.h.removed)
	}
}

// fakeGitHub answers MergedHeads with a fixed list or error, and Protected with protected.
type fakeGitHub struct {
	heads     []string
	err       error
	protected bool
}

func (g fakeGitHub) MergedHeads(ctx context.Context, repo, branch string) ([]string, error) {
	return g.heads, g.err
}

func (g fakeGitHub) Protected(ctx context.Context, repo string) (bool, error) {
	return g.protected, g.err
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

// A launch too long for the tty is refused before spawn makes a worktree or a record, so
// it leaves nothing half-built (bees-8.3 review). A deep cache dir makes the paths long.
func TestSpawnRefusesALongLaunchBeforeTouchingAnything(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	deep := filepath.Join(t.TempDir(), strings.Repeat("d", 200), strings.Repeat("e", 200))
	t.Setenv("XDG_CACHE_HOME", deep)
	t.Setenv("HOME", deep)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); !errors.Is(err, worker.ErrLongLaunch) {
		t.Fatalf("err = %v, want ErrLongLaunch", err)
	}
	if _, ok, _ := r.lb.LatestSpawn(ctx, "bees-8"); ok || len(r.h.workspaces) != 0 {
		t.Fatalf("recorded = %v, workspaces = %v; want nothing", ok, r.h.workspaces)
	}
}

// bees-11.4: the launch check counts --model, so a model too long to type is refused
// before anything is made, like a deep cache dir.
func TestSpawnCountsTheModelInTheLaunchBudget(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.fleet.Harness = claude.New(config.ClaudeCode{Model: strings.Repeat("m", 768)})
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); !errors.Is(err, worker.ErrLongLaunch) {
		t.Fatalf("err = %v, want ErrLongLaunch", err)
	}
	if _, ok, _ := r.lb.LatestSpawn(ctx, "bees-8"); ok || len(r.h.workspaces) != 0 {
		t.Fatalf("recorded = %v, workspaces = %v; want nothing", ok, r.h.workspaces)
	}
}

// The settings file gets a fresh private name per start, not a fixed one another process
// could swap before Claude reads it, and goes away with the worker (bees-8.3 review).
func TestSettingsFileIsPrivateAndGoesWithTheWorker(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	start := r.h.started[0]
	path := start[slices.Index(start, "--settings")+1]
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 || filepath.Base(path) == "bees-8-settings.json" {
		t.Fatalf("settings %s: %v, %v; want a fresh 0600 file", path, fi, err)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("settings %s after bs end: %v; want it gone", path, err)
	}
	r.h.exit("bees-8")
	r.h.failStart = errors.New("herdr down")
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "bees-8-settings*"))
	if len(left) != 0 {
		t.Fatalf("after a failed start: %v; want no settings file", left)
	}
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
			if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
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
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
		t.Fatal(err)
	}
	if !branchExists(r.repo, "bees-8") {
		t.Fatal("bs end deleted a branch; only prune may")
	}
}

// A branch checked out in any worktree may hold live work, even with no commits yet:
// another session took over an ended worker's branch, and prune tried to delete it
// (bees-8.4).
func TestPruneKeepsABranchCheckedOutInAWorktree(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	gitRun(t, "", "init", "-q", "-b", "main", r.repo)
	gitCommit(t, r.repo, "init", map[string]string{"a.txt": "A"})
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	_ = r.fleet.End(ctx, "bees-8", worker.EndOpts{})
	wt, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wt = filepath.Join(wt, "taken-over")
	gitRun(t, r.repo, "worktree", "add", "-q", "-b", "bees-8", wt) // no commits of its own: tip == main
	pruned, err := r.fleet.Prune(ctx, false)
	if err != nil || len(pruned) != 1 {
		t.Fatalf("prune: %+v, %v", pruned, err)
	}
	if p := pruned[0]; p.Deleted || p.InUse != wt || !branchExists(r.repo, "bees-8") {
		t.Fatalf("want kept, in use at %s: %+v, branch exists=%v", wt, p, branchExists(r.repo, "bees-8"))
	}
}

// A rebase or bisect detaches the worktree's HEAD, so worktree list says "detached", but
// git still counts the branch as in use there (bees-8.4 review).
func TestPruneKeepsABranchMidRebaseInAWorktree(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	gitRun(t, "", "init", "-q", "-b", "main", r.repo)
	gitCommit(t, r.repo, "init", map[string]string{"a.txt": "A"})
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	_ = r.fleet.End(ctx, "bees-8", worker.EndOpts{})
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(tmp, "rebasing")
	gitRun(t, r.repo, "worktree", "add", "-q", "-b", "bees-8", wt)
	gitCommit(t, wt, "work", map[string]string{"a.txt": "branch"})
	gitCommit(t, r.repo, "main moves", map[string]string{"a.txt": "main"})
	_ = exec.Command("git", "-C", wt, "-c", "user.email=t@t", "-c", "user.name=t", "rebase", "main").Run() // stops on the conflict
	pruned, err := r.fleet.Prune(ctx, false)
	if err != nil || len(pruned) != 1 {
		t.Fatalf("prune: %+v, %v", pruned, err)
	}
	if p := pruned[0]; p.Deleted || p.InUse != wt || !branchExists(r.repo, "bees-8") {
		t.Fatalf("want kept, in use at %s: %+v", wt, p)
	}
}

// A worktree whose directory is gone still holds its branch until git worktree prune;
// prune says so instead of keeping the branch forever without a hint.
func TestPruneSaysWhenTheWorktreeHoldingABranchIsGone(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	gitRun(t, "", "init", "-q", "-b", "main", r.repo)
	gitCommit(t, r.repo, "init", map[string]string{"a.txt": "A"})
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	_ = r.fleet.End(ctx, "bees-8", worker.EndOpts{})
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(tmp, "gone")
	gitRun(t, r.repo, "worktree", "add", "-q", "-b", "bees-8", wt)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	pruned, err := r.fleet.Prune(ctx, false)
	if err != nil || len(pruned) != 1 {
		t.Fatalf("prune: %+v, %v", pruned, err)
	}
	if p := pruned[0]; p.Deleted || p.InUse != wt || !p.Prunable {
		t.Fatalf("want kept, in use at %s and prunable: %+v", wt, p)
	}
}

func TestPruneDryRunDeletesNothing(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	squashMerged(t, r)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	_ = r.fleet.End(ctx, "bees-8", worker.EndOpts{})
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
	_ = r.fleet.End(ctx, "bees-8", worker.EndOpts{})
	r.h.exit("bees-8")
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil { // live once more
		t.Fatal(err)
	}
	if pruned, err := r.fleet.Prune(ctx, false); err != nil || len(pruned) != 0 {
		t.Fatalf("respawned worker: %+v, %v", pruned, err)
	}
	_ = r.fleet.End(ctx, "bees-8", worker.EndOpts{})
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

// gitInit makes dir an empty git repo.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "-q", "-b", "main")
}

// workerPrompt is the worker prompt under the default delivery, github_pr.
func workerPrompt(t *testing.T) string {
	t.Helper()
	d, err := prompt.Delivery("github_pr", "")
	if err != nil {
		t.Fatal(err)
	}
	text, err := prompt.Text(prompt.Worker, "bees", "", d)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// bees-12.10: a spawn pins no env (ADR-0009 amendment 1): the worker's bs finds its
// project, name and logbook from its herdr pane, so --settings carries no env block.
func TestSpawnPinsNoEnv(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsFile(t, r.h.started[0]), &settings); err != nil {
		t.Fatal(err)
	}
	if env, ok := settings["env"]; ok {
		t.Errorf("--settings env = %v, want none", env)
	}
}

// A worker's worktree lives under paths.worktrees, cut from the HEAD of the base (a
// linked worktree named by --repo or paths.repo) but run from the main checkout, since
// herdr refuses a linked worktree as --cwd; its spawn records both (bees-11.3).
func TestSpawnCutsFromTheBaseUnderTheWorktreesDir(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	gitRun(t, "", "init", "-q", "-b", "main", r.repo)
	gitCommit(t, r.repo, "init", nil)
	base := filepath.Join(t.TempDir(), "bees-1")
	gitRun(t, r.repo, "worktree", "add", "-q", "-b", "bees-1", base)
	gitCommit(t, base, "base only", nil)
	trees := t.TempDir()
	r.fleet.P.Worktrees, r.fleet.P.Base = trees, base
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8")
	cut := [][2]string{{r.repo, gitTip(t, base, "HEAD")}}
	if want := filepath.Join(trees, "bees-8"); sp.Worktree != want || sp.Base != base || sp.Repo != r.repo || !slices.Equal(r.h.cuts, cut) {
		t.Errorf("spawn %+v cut %q; want worktree %s, base %s, cut %q", sp, r.h.cuts, want, base, cut)
	}
}

// An orphan is a workspace on the worker's current worktree path, or on the one its
// latest spawn recorded: paths.worktrees may have moved since it ended.
func TestAnOrphanAtTheRecordedWorktreeIsFound(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
		t.Fatal(err)
	}
	r.h.workspaces = []worker.Workspace{{ID: "w9", Path: r.repo + "-WT/bees-8"}} // reopened by hand
	r.fleet.P.Worktrees = t.TempDir()
	if _, err := r.fleet.Spawn(ctx, "bees-8", ""); !errors.Is(err, worker.ErrOrphan) || !strings.Contains(err.Error(), "w9") {
		t.Fatalf("err = %v, want ErrOrphan naming w9", err)
	}
}

// bees-11.4: [worker.claude_code] model reaches every start, a resume included; empty
// leaves Claude's own default.
func TestWorkersStartWithTheConfiguredModel(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.fleet.Harness = claude.New(config.ClaudeCode{Model: "claude-sonnet-5-5"})
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.exit("bees-8")
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if len(r.h.started) != 2 {
		t.Fatalf("started = %q, want a start and a restart", r.h.started)
	}
	for _, start := range r.h.started {
		if !slices.Contains(start, "--model=claude-sonnet-5-5") {
			t.Errorf("start = %q, want --model=claude-sonnet-5-5", start)
		}
	}

	r = newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if slices.ContainsFunc(r.h.started[0], func(a string) bool { return strings.HasPrefix(a, "--model") }) {
		t.Errorf("start = %q, want no --model when none is configured", r.h.started[0])
	}
}

// spawnSettings spawns bees-8 and returns its deny rules, its autoMode allow and its prompt.
func spawnSettings(t *testing.T, r rig) (deny, allow []string, promptText string) {
	t.Helper()
	if _, err := r.fleet.Spawn(context.Background(), "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	start := r.h.started[len(r.h.started)-1]
	var settings struct {
		AutoMode    map[string][]string `json:"autoMode"`
		Permissions struct{ Deny []string }
	}
	if err := json.Unmarshal(settingsFile(t, start), &settings); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(start[slices.Index(start, "--append-system-prompt-file")+1])
	if err != nil {
		t.Fatal(err)
	}
	return settings.Permissions.Deny, settings.AutoMode["allow"], string(b)
}

func deniedBy(deny []string, cmd string) bool {
	return slices.ContainsFunc(deny, func(rule string) bool { return claudeRuleMatches(rule, cmd) })
}

// The guards deny pushes to the repo's real default branch, not only main and master
// (bees-11.7).
func TestGuardsDenyPushesToTheReposRealDefaultBranch(t *testing.T) {
	r := newRig(t)
	gitRun(t, r.repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")
	deny, _, _ := spawnSettings(t, r)
	for _, cmd := range []string{"git push origin develop", "git push origin bees-8:develop", "git push origin main"} {
		if !deniedBy(deny, cmd) {
			t.Errorf("deny rules let through %q", cmd)
		}
	}
}

// A repo that was never cloned has no origin/HEAD: the branch its main checkout has out
// is guarded too, so a trunk-based repo isn't left open (bees-11.7 review).
func TestGuardsDenyPushesToTheMainCheckoutsBranch(t *testing.T) {
	r := newRig(t)
	gitRun(t, r.repo, "symbolic-ref", "HEAD", "refs/heads/trunk")
	deny, _, _ := spawnSettings(t, r)
	if !deniedBy(deny, "git push origin trunk") {
		t.Errorf("deny rules let through a push to the checked-out trunk: %q", deny)
	}
}

// A fragment path may start with ~, like every path in bs's config.
func TestFragmentPathExpandsTilde(t *testing.T) {
	r := newRig(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "cl.md"), []byte("Mail a CL.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.fleet.Delivery.Fragment = "~/cl.md"
	if _, _, text := spawnSettings(t, r); !strings.Contains(text, "Mail a CL.") {
		t.Errorf("worker prompt:\n%s", text)
	}
}

// Under manual delivery a worker gets the manual fragment and guards, and bs never asks
// GitHub anything: not whether main is protected, not whether a PR merged.
func TestManualDeliveryNeverAsksGitHub(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	gitCommit(t, r.repo, "init", map[string]string{"a.txt": "A"})
	r.fleet.Delivery.Kind = "manual"
	r.fleet.G = fakeGitHub{protected: true, heads: []string{gitTip(t, r.repo, "main")}}
	deny, allow, text := spawnSettings(t, r)
	if deniedBy(deny, "gh pr merge 41") || !deniedBy(deny, "git push origin main") {
		t.Errorf("manual deny = %q", deny)
	}
	if a := strings.Join(allow, " "); strings.Contains(a, "pull request") {
		t.Errorf("manual allow = %q", allow)
	}
	if !strings.Contains(text, "Leave `bd close` to the keeper") || strings.Contains(text, "PR #N") {
		t.Errorf("manual worker prompt:\n%s", text)
	}
	// GitHub would call bees-8 merged (a merged PR has its tip as head); git alone can't.
	gitRun(t, r.repo, "checkout", "-q", "-b", "bees-8")
	gitCommit(t, r.repo, "work", map[string]string{"c.txt": "C"})
	gitRun(t, r.repo, "checkout", "-q", "main")
	r.fleet.G = fakeGitHub{heads: []string{gitTip(t, r.repo, "bees-8")}}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
		t.Fatal(err)
	}
	pruned, err := r.fleet.Prune(ctx, false)
	if err != nil || len(pruned) != 1 || pruned[0].Deleted || strings.Contains(pruned[0].Why, "GitHub") {
		t.Fatalf("manual prune = %+v, %v; want bees-8 kept on git's word alone", pruned, err)
	}
}

// An unreadable delivery fragment, or a merge grant that covers the default branch,
// refuses the spawn before a worktree or record is made.
func TestBadDeliveryOrGuardsRefuseTheSpawnBeforeAnythingIsMade(t *testing.T) {
	for name, set := range map[string]func(*worker.Fleet){
		"missing fragment": func(f *worker.Fleet) { f.Delivery.Fragment = "/nonexistent/cl.md" },
		"merge into the default": func(f *worker.Fleet) {
			f.Guards.AllowMergeInto, f.G = []string{"ma*"}, fakeGitHub{protected: true}
		},
		// A merge grant fails closed: only branch protection keeps the merge off main.
		"merge grant, main unprotected": func(f *worker.Fleet) {
			f.Guards.AllowMergeInto, f.G = []string{"pilot/*"}, fakeGitHub{protected: false}
		},
		"merge grant, GitHub unreachable": func(f *worker.Fleet) {
			f.Guards.AllowMergeInto, f.G = []string{"pilot/*"}, fakeGitHub{protected: true, err: errors.New("offline")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			set(&r.fleet)
			if _, err := r.fleet.Spawn(context.Background(), "bees-8", "spawn and end"); err == nil {
				t.Fatal("spawned")
			}
			if _, ok, _ := r.lb.LatestSpawn(context.Background(), "bees-8"); ok || len(r.h.workspaces) != 0 {
				t.Errorf("a refused spawn left a record (%v) or a workspace (%+v)", ok, r.h.workspaces)
			}
		})
	}
}

// A fragment file is the worker's delivery wording, and a merge grant reaches both the
// prompt and the guards (bees-12.13).
func TestFragmentAndMergeGrantReachTheWorker(t *testing.T) {
	r := newRig(t)
	frag := filepath.Join(t.TempDir(), "cl.md")
	if err := os.WriteFile(frag, []byte("Mail a CL.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.fleet.Delivery.Fragment = frag
	r.fleet.Guards.AllowMergeInto = []string{"pilot/*"}
	r.fleet.G = fakeGitHub{protected: true}
	deny, allow, text := spawnSettings(t, r)
	if !strings.Contains(text, "## Delivery\n\nMail a CL.\n") || !strings.Contains(text, "`pilot/*`") || strings.Contains(text, "PR #N") {
		t.Errorf("worker prompt:\n%s", text)
	}
	if deniedBy(deny, "gh pr merge 41") || !deniedBy(deny, "git push origin main") {
		t.Errorf("deny = %q", deny)
	}
	if !strings.Contains(strings.Join(allow, " "), "pilot/*") {
		t.Errorf("allow = %q", allow)
	}
}

// fakeHarness is a harness.Harness whose launch is files: one per key in files, each
// passed as --<key> {{key}}. With noResume it refuses a start that would continue.
type fakeHarness struct {
	files    map[string]string
	noResume bool
}

func (fakeHarness) Kind() string { return "fake" }

func (h fakeHarness) Prepare(st harness.Start) (harness.Launch, error) {
	if h.noResume && st.Continue {
		return harness.Launch{}, harness.ErrNoResume
	}
	l := harness.Launch{Files: h.files, Record: map[string]string{"profile": st.Name}}
	for k := range h.files {
		l.Args = append(l.Args, "--"+k, "{{"+k+"}}")
	}
	return l, nil
}

func (fakeHarness) Ring(harness.Target, string, harness.Priority) error { return nil }

// bees-12.6: the core writes a harness's files, fills their paths into its argv, starts
// herdr's kind for it and records its name and Record in the spawned entry.
func TestSpawnWritesTheHarnessFilesAndRecordsTheHarness(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.fleet.Harness, r.fleet.HarnessName = fakeHarness{files: map[string]string{"overlay.yml": "a: 1", "ext": "x"}}, "fake_harness"
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	start := r.h.started[0]
	path := start[slices.Index(start, "--overlay.yml")+1]
	if b, err := os.ReadFile(path); err != nil || string(b) != "a: 1" || !strings.HasPrefix(filepath.Base(path), "bees-8-overlay-") || filepath.Ext(path) != ".yml" {
		t.Fatalf("overlay %s = %q, %v", path, b, err)
	}
	if r.h.kind != "fake" {
		t.Errorf("herdr kind = %q, want the harness's Kind", r.h.kind)
	}
	sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8")
	if sp.Harness != "fake_harness" || sp.Record["profile"] != "bees-8" {
		t.Errorf("spawned entry = %+v, want harness fake_harness and its Record", sp)
	}
}

// bees-12.12: the launch check measures a resumed start, which a harness that can't resume
// (agy) refuses; a first spawn never resumes, so it goes ahead on the fresh start's line.
func TestAHarnessThatCantResumeStillSpawnsFresh(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.fleet.Harness = fakeHarness{noResume: true}
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatalf("err = %v, want a fresh spawn", err)
	}
}

// bees-12.6: a harness file key that breaks harness.FileKey is refused before anything is
// made, since bs end's cleanup glob relies on it.
func TestABadHarnessFileKeyIsRefusedBeforeTouchingAnything(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.fleet.Harness = fakeHarness{files: map[string]string{"1x": ""}}
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err == nil || !strings.Contains(err.Error(), `"1x"`) {
		t.Fatalf("err = %v, want the bad key refused", err)
	}
	if _, ok, _ := r.lb.LatestSpawn(ctx, "bees-8"); ok || len(r.h.workspaces) != 0 {
		t.Fatalf("recorded = %v, workspaces = %v; want nothing", ok, r.h.workspaces)
	}
}

// bees-12.6: ending bees-8 removes its files and leaves those of bees-8-1, whose name
// extends it, and the project's prompt file.
func TestEndingAWorkerLeavesTheFilesOfAWorkerWhoseNameExtendsIt(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	for _, b := range []string{"bees-8", "bees-8.1"} {
		if _, err := r.fleet.Spawn(ctx, b, "spawn and end"); err != nil {
			t.Fatal(err)
		}
	}
	child := r.h.started[1][slices.Index(r.h.started[1], "--settings")+1]
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(child); err != nil {
		t.Fatalf("bees-8-1's settings %s after ending bees-8: %v", child, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(child), "bees-worker.md")); err != nil {
		t.Fatalf("the project's prompt file after ending bees-8: %v", err)
	}
}

// pathOf returns the dirs a start's SessionStart hook puts first on the worker's PATH.
func pathOf(t *testing.T, start []string) string {
	t.Helper()
	var s struct {
		Hooks map[string][]struct{ Hooks []struct{ Command string } }
	}
	if err := json.Unmarshal(settingsFile(t, start), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Hooks["SessionStart"]) == 0 {
		return ""
	}
	cmd := s.Hooks["SessionStart"][0].Hooks[0].Command
	_, rest, _ := strings.Cut(cmd, `PATH="`)
	dirs, _, _ := strings.Cut(rest, ":$PATH")
	return dirs
}

// bees-8.19: --path puts a build first on a worker's PATH, so a live test's workers run
// the build under test. The spawn records it, and a restart or a respawn after an end
// reuses it, as it does the label.
func TestSpawnPathIsRecordedAndKeptOnRestart(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	bin := t.TempDir()
	if res := r.fleet.SpawnAll(ctx, []worker.Request{{Bead: "bees-8", Label: "spawn and end", Path: []string{bin}}}); res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); !slices.Equal(sp.Path, []string{bin}) {
		t.Fatalf("recorded path = %q, want [%s]", sp.Path, bin)
	}
	if got := pathOf(t, r.h.started[0]); got != bin {
		t.Fatalf("first start's PATH dirs = %q, want %s", got, bin)
	}
	r.h.exit("bees-8")
	if res := r.fleet.SpawnAll(ctx, []worker.Request{{Bead: "bees-8", Path: []string{t.TempDir()}}}); !errors.Is(res[0].Err, worker.ErrPathChange) || !worker.Refused(res[0].Err) {
		t.Fatalf("restart with another path: err = %v, want a refused ErrPathChange", res[0].Err)
	}
	if res := r.fleet.SpawnAll(ctx, []worker.Request{{Bead: "bees-8"}}); res[0].Err != nil || !res[0].Started.Resumed {
		t.Fatalf("restart = %+v", res[0])
	}
	if got := pathOf(t, r.h.started[1]); got != bin {
		t.Fatalf("restart's PATH dirs = %q, want the recorded %s", got, bin)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.fleet.Spawn(ctx, "bees-8", ""); err != nil {
		t.Fatal(err)
	}
	if got := pathOf(t, r.h.started[2]); got != bin {
		t.Errorf("respawn after end: PATH dirs = %q, want the recorded %s", got, bin)
	}
	// An explicit --path after an end replaces it.
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if res := r.fleet.SpawnAll(ctx, []worker.Request{{Bead: "bees-8", Path: []string{other}}}); res[0].Err != nil || pathOf(t, r.h.started[3]) != other {
		t.Errorf("respawn with --path %s: err = %v", other, res[0].Err)
	}
}

// bees-12.24: worker.path is the --path of a spawn that passes none, so a live test's toy
// queen spawns the build under test without naming it. A recorded worker keeps its own.
func TestConfiguredPathIsTheDefaultForNewWorkers(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if res := r.fleet.SpawnAll(ctx, []worker.Request{{Bead: "bees-8", Label: "spawn and end"}}); res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	r.h.exit("bees-8")
	r.fleet.DefaultPath = []string{t.TempDir()}
	if res := r.fleet.SpawnAll(ctx, []worker.Request{{Bead: "bees-8"}}); res[0].Err != nil || pathOf(t, r.h.started[1]) != "" {
		t.Fatalf("restart of a worker recorded without a path: err = %v, started = %q", res[0].Err, r.h.started[1])
	}
	bin := r.fleet.DefaultPath[0]
	if res := r.fleet.SpawnAll(ctx, []worker.Request{{Bead: "bees-9", Label: "new one"}}); res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-9"); !slices.Equal(sp.Path, []string{bin}) || pathOf(t, r.h.started[2]) != bin {
		t.Errorf("new worker: recorded %q, started on %q; want worker.path %s", sp.Path, pathOf(t, r.h.started[2]), bin)
	}
	other := t.TempDir()
	if res := r.fleet.SpawnAll(ctx, []worker.Request{{Bead: "bees-10", Label: "flag wins", Path: []string{other}}}); res[0].Err != nil || pathOf(t, r.h.started[3]) != other {
		t.Errorf("--path %s over worker.path: err = %v", other, res[0].Err)
	}
}

// A recorded --path dir that has gone (a live test's build removed) refuses the restart,
// rather than quietly starting the worker on the shared bs.
func TestRestartRefusesARecordedPathThatHasGone(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if res := r.fleet.SpawnAll(ctx, []worker.Request{{Bead: "bees-8", Label: "spawn and end", Path: []string{bin}}}); res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	r.h.exit("bees-8")
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if _, err := r.fleet.Spawn(ctx, "bees-8", ""); !errors.Is(err, worker.ErrBadPath) || len(r.h.started) != 1 {
		t.Fatalf("err = %v, starts = %d; want ErrBadPath and no start", err, len(r.h.started))
	}
}

func TestSpawnRefusesABadPathBeforeTouchingAnything(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "bs")
	if err := os.WriteFile(file, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, c := range []string{"it's", "a:b", "$HOME", "a`b", "a\nb", `a"b`, `a\b`} {
		d := filepath.Join(t.TempDir(), c)
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, d)
	}
	for _, p := range append([]string{"bin", filepath.Join(t.TempDir(), "missing"), file}, dirs...) {
		r := newRig(t)
		res := r.fleet.SpawnAll(ctx, []worker.Request{{Bead: "bees-8", Label: "spawn and end", Path: []string{p}}})
		if !errors.Is(res[0].Err, worker.ErrBadPath) || !worker.Refused(res[0].Err) {
			t.Errorf("--path %q: err = %v, want a refused ErrBadPath", p, res[0].Err)
		}
		if len(r.h.workspaces) != 0 {
			t.Errorf("--path %q: a workspace was made", p)
		}
	}
}

// bees-12.10: a confirmed start records the harness process herdr shows in the pane,
// with its start time and baseline, then names the agent in herdr.
func TestAConfirmedStartRecordsTheHarnessProcess(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8")
	st, ok, err := r.lb.LatestStart(ctx, sp.ID)
	pid := r.h.pids["bees-8"]
	if err != nil || !ok || st.PID != pid || st.Start != r.h.procs[pid].start || st.Harness != "claude_code" || st.Baseline == nil {
		t.Fatalf("start = %+v, %v, %v; want pid %d and its start time", st, ok, err, pid)
	}
	if !slices.Equal(r.h.renamed, []string{sp.Pane + "=bees-8"}) {
		t.Errorf("renamed = %v, want the pane named bees-8 once", r.h.renamed)
	}
}

// bees-12.10 (T4): the recorded pid reused by another process (another start time) is
// not the worker, so it may be started again; the live one is a twin.
func TestAReusedPidIsNotTheWorker(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); !errors.Is(err, worker.ErrRunning) {
		t.Fatalf("respawn of a live worker: err = %v, want ErrRunning", err)
	}
	r.h.mu.Lock()
	*r.h.procs[r.h.pids["bees-8"]] = fakeProc{name: "vim", start: "reused", parent: 1, alive: true} // another program got the pid
	delete(r.h.agents, "bees-8")
	r.h.mu.Unlock()
	if st, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil || !st.Resumed {
		t.Fatalf("respawn over a reused pid = %+v, %v; want a restart", st, err)
	}
}

// bees-12.10: a spawn with no start recorded (from before bs recorded starts) reads as
// Absent, but its start looks in the pane first: a harness already there refuses it, so an
// upgrade never starts a twin; herdr down fails closed.
func TestAStartRefusesAHarnessAlreadyInThePane(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	ws, _ := r.h.CreateWorktree(ctx, r.repo, "", "bees-8", filepath.Join(r.repo+"-WT", "bees-8"), "spawn and end")
	if _, err := r.lb.RecordSpawn(ctx, "queen-bees", logbook.Spawn{Name: "bees-8", Bead: "bees-8", Label: "spawn and end", Workspace: ws.ID, Pane: ws.Pane, Worktree: ws.Path, Branch: "bees-8", Repo: r.repo}, ""); err != nil {
		t.Fatal(err)
	}
	r.h.mu.Lock()
	r.h.pids["bees-8"], r.h.agents["bees-8"] = r.h.spawnProc("claude", shell(ws.Pane)), ws.Pane
	r.h.mu.Unlock()
	if _, err := r.fleet.Spawn(ctx, "bees-8", ""); !errors.Is(err, worker.ErrRunning) {
		t.Fatalf("err = %v, want ErrRunning from the pane's claude", err)
	}
	r.h.listErr = fmt.Errorf("%w: no server", worker.ErrHerdrDown)
	if _, err := r.fleet.Spawn(ctx, "bees-8", ""); !errors.Is(err, worker.ErrHerdrDown) {
		t.Fatalf("herdr down: err = %v, want ErrHerdrDown (fail closed)", err)
	}
	r.h.listErr = nil
	r.h.exit("bees-8")
	if st, err := r.fleet.Spawn(ctx, "bees-8", ""); err != nil || !st.Resumed {
		t.Fatalf("after it exited: %+v, %v; want a restart", st, err)
	}
}

// bees-12.10: a worker whose workspace is gone while its process still runs was moved
// (herdr pane move): bs end refuses rather than record ended over it; --force ends it.
func TestEndRefusesAMovedWorker(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.movePane("bees-8")
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); !errors.Is(err, worker.ErrMoved) || !strings.Contains(err.Error(), fmt.Sprint(r.h.pids["bees-8"])) {
		t.Fatalf("err = %v, want ErrMoved naming the pid", err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); sp.Ended {
		t.Fatal("recorded ended over a running worker")
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{Force: true}); err != nil {
		t.Fatal(err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); !sp.Ended {
		t.Fatal("--force didn't end it")
	}
}

// bees-12.10: a resume whose start was never confirmed leaves the previous start's dead
// pid newest; the harness it launched may still come up in the pane, and then the next
// spawn refuses instead of starting a twin beside it (motif 7).
func TestAnUnconfirmedResumeIsNotTwinned(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	defer worker.Fast(20 * time.Millisecond)()
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.exit("bees-8")
	r.h.failStart = fmt.Errorf("herdr agent: timed out: %w", worker.ErrUnconfirmed)
	if _, err := r.fleet.Spawn(ctx, "bees-8", ""); !errors.Is(err, worker.ErrUnconfirmed) {
		t.Fatalf("err = %v, want ErrUnconfirmed", err)
	}
	r.h.failStart = nil
	r.h.mu.Lock() // it came up late, after bs stopped looking
	r.h.pids["bees-8"], r.h.agents["bees-8"] = r.h.spawnProc("claude", shell("w1:p1")), "w1:p1"
	r.h.mu.Unlock()
	if _, err := r.fleet.Spawn(ctx, "bees-8", ""); !errors.Is(err, worker.ErrRunning) || !strings.Contains(err.Error(), "no start of") {
		t.Fatalf("err = %v, want ErrRunning for the unrecorded harness in the pane", err)
	}
}

// bees-12.10: a harness that shows below the pane's shell only after herdr gave up (busy
// with its doorbell) is still found within the wait, as a grandchild too.
func TestALateHarnessBelowTheShellIsConfirmed(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.h.failStart = fmt.Errorf("herdr agent: timed out: %w", worker.ErrUnconfirmed)
	go func() {
		time.Sleep(300 * time.Millisecond)
		r.h.mu.Lock()
		wrapper := r.h.spawnProc("sh", shell("w1:p1")) // e.g. a wrapper script
		r.h.pids["bees-8"] = r.h.spawnProc("claude", wrapper)
		r.h.mu.Unlock()
	}()
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8")
	if st, ok, _ := r.lb.LatestStart(ctx, sp.ID); !ok || st.PID != r.h.pids["bees-8"] {
		t.Fatalf("start = %+v, %v; want the late grandchild", st, ok)
	}
}

// bees-12.36: a worker's pane moved into another worker's workspace dies when that
// workspace closes, so bs end of the workspace's owner refuses while it holds the guest,
// and --forget records the end and leaves the workspace open (motif 12).
func TestEndKeepsAWorkspaceThatHoldsAnotherWorkersPane(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	_, _ = r.fleet.Spawn(ctx, "bees-9", "second worker")
	r.h.movePaneInto("bees-9", "w1") // bees-9 now runs in bees-8's workspace
	err := r.fleet.End(ctx, "bees-8", worker.EndOpts{Force: true})
	if !errors.Is(err, worker.ErrGuest) || strings.Count(err.Error(), "bees-9 (pid") != 1 {
		t.Fatalf("err = %v, want ErrGuest naming bees-9", err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); sp.Ended || len(r.h.removed) != 0 {
		t.Fatalf("ended = %v, removed = %v; want nothing touched", sp.Ended, r.h.removed)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{Forget: true}); err != nil {
		t.Fatal(err)
	}
	sp8, _, _ := r.lb.LatestSpawn(ctx, "bees-8")
	sp9, _, _ := r.lb.LatestSpawn(ctx, "bees-9")
	if alive, _, _, err := worker.SpawnAlive(ctx, r.lb, r.h, sp9); err != nil || !alive || len(r.h.removed) != 0 {
		t.Fatalf("bees-9 alive = %v (%v), removed = %v; want it running in the open workspace", alive, err, r.h.removed)
	}
	if alive, _, _, _ := worker.SpawnAlive(ctx, r.lb, r.h, sp8); alive || !slices.Equal(r.h.closedPanes, []string{sp8.Pane}) {
		t.Fatalf("bees-8 alive = %v, closed panes = %v; want only its own pane closed", alive, r.h.closedPanes)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); !sp.Ended {
		t.Fatal("--forget didn't record the end")
	}
}

// bees-12.36: when herdr can't list the workspace's panes, bs end closes nothing.
func TestEndClosesNothingWhenItCantListThePanes(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	_, _ = r.fleet.Spawn(ctx, "bees-9", "second worker")
	h := panesDown{r.h}
	r.fleet.H = h
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); !errors.Is(err, worker.ErrHerdrDown) || len(r.h.removed) != 0 {
		t.Fatalf("err = %v, removed = %v; want ErrHerdrDown and nothing closed", err, r.h.removed)
	}
}

// panesDown is fakeHerdr whose pane list fails.
type panesDown struct{ *fakeHerdr }

func (panesDown) Panes(context.Context, string) ([]string, error) {
	return nil, fmt.Errorf("%w: herdr pane list: no server", worker.ErrHerdrDown)
}

// bees-12.36: an unrecorded harness (another project's session, a keeper's Claude) moved
// into the workspace is a guest too; and ending every worker at once with --force, in any
// order, kills none whose pane was moved: --force overrides a dirty worktree, not a guest.
func TestEndAllForceKillsNoGuest(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	_, _ = r.fleet.Spawn(ctx, "bees-9", "second worker")
	r.h.movePaneInto("bees-8", "w2") // bees-8 now runs in bees-9's workspace; its own closed
	r.h.mu.Lock()
	stranger := r.h.spawnProc("claude", 0)
	r.h.agents["stranger"], r.h.pids["stranger"] = "w2:p77", stranger
	r.h.moved["w2:p77"] = 0
	r.h.mu.Unlock()
	es, err := r.fleet.EndAll(ctx, worker.EndOpts{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, e := range es {
		if e.Err != nil {
			kept = append(kept, e.Name+": "+e.Err.Error())
		}
	}
	if len(kept) != 1 || !strings.Contains(kept[0], "bees-9") || !strings.Contains(kept[0], "a claude process (pid") || len(r.h.removed) != 0 {
		t.Fatalf("kept = %q, removed = %v; want bees-9 kept for its guests", kept, r.h.removed)
	}
	r.h.mu.Lock()
	alive := r.h.procs[stranger].alive && r.h.procs[r.h.pids["bees-8"]].alive
	r.h.mu.Unlock()
	if !alive {
		t.Fatal("a guest was killed")
	}
}

// child runs a process named name under worker's harness and returns its pid.
func (h *fakeHerdr) child(worker, name string, ports ...int) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	pid := h.spawnProc(name, h.pids[worker])
	h.procs[pid].ports = ports
	return pid
}

// T10: a child the start didn't see refuses bs end, naming its pid, command and port; the
// worker stays live with its workspace; --force ends it.
func TestEndRefusesBackgroundWork(t *testing.T) {
	defer worker.FastGate(time.Millisecond)()
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	pid := r.h.child("bees-8", "python3 -m http.server 8000", 8000)
	err := r.fleet.End(ctx, "bees-8", worker.EndOpts{})
	var bg *worker.BackgroundError
	if !errors.As(err, &bg) || !errors.Is(err, worker.ErrBackground) || len(bg.Procs) != 1 || bg.Procs[0].PID != pid {
		t.Fatalf("err = %v, want ErrBackground naming pid %d", err, pid)
	}
	if want := fmt.Sprintf("pid %-6d python3 -m http.server 8000   listening :8000", pid); !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want a line %q", err, want)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); sp.Ended || len(r.h.removed) > 0 {
		t.Fatalf("ended %v, removed %v: want the worker kept", sp.Ended, r.h.removed)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{Force: true}); err != nil {
		t.Fatal(err)
	}
}

// T10: the baseline's helpers, this bs and its ancestors, and a child gone by the second
// look are not background work.
func TestEndPassesBaselineCallerAndShortLivedChildren(t *testing.T) {
	defer worker.FastGate(50 * time.Millisecond)()
	ctx := context.Background()
	r := newRig(t)
	r.h.helpers = []string{"mcp-server"}
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.mu.Lock()
	shellPID := r.h.spawnProc("zsh", r.h.pids["bees-8"]) // a Bash tool call running this bs
	r.h.procs[os.Getpid()] = &fakeProc{name: "bs", start: "self", parent: shellPID, alive: true}
	r.h.mu.Unlock()
	hook := r.h.child("bees-8", "statusline")
	go func() {
		time.Sleep(10 * time.Millisecond)
		r.h.mu.Lock()
		r.h.procs[hook].alive = false
		r.h.mu.Unlock()
	}()
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); err != nil {
		t.Fatalf("err = %v, want the end to pass the gate", err)
	}
}

// T10: a walk that fails refuses with ErrCantCheck, never as "no background work".
func TestEndFailsClosedWhenTheWalkFails(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.walkErr = errors.New("read /proc: too many open files")
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); !errors.Is(err, worker.ErrCantCheck) || errors.Is(err, worker.ErrBackground) {
		t.Fatalf("err = %v, want ErrCantCheck", err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8"); sp.Ended {
		t.Fatal("ended a worker the gate couldn't check")
	}
}

// A worker whose recorded process has exited has nothing to walk: bs end says so and ends it.
func TestEndOfAnExitedWorkerSkipsTheGate(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	r.h.child("bees-8", "orphan")
	r.h.mu.Lock()
	r.h.procs[r.h.pids["bees-8"]].alive = false
	r.h.mu.Unlock()
	e, err := r.fleet.EndOne(ctx, "bees-8", worker.EndOpts{})
	if err != nil || !e.Exited {
		t.Fatalf("ending = %+v, err = %v; want ended, noted as exited", e, err)
	}
}

// A spawn with no start recorded can't be checked while its pane runs the harness.
func TestEndOfAnUnrecordedHarnessFailsClosed(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	sp, _, _ := r.lb.LatestSpawn(ctx, "bees-8")
	db, err := sql.Open("sqlite", r.fleet.P.Logbook) // as a bs from before starts left it
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `DELETE FROM entries WHERE kind = 'started' AND ref = ?`, string(sp.ID)); err != nil {
		t.Fatal(err)
	}
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{}); !errors.Is(err, worker.ErrCantCheck) || !errors.Is(err, worker.ErrNoStart) {
		t.Fatalf("err = %v, want ErrCantCheck: no start, and the pane runs claude", err)
	}
}

// T10: bs end --all gates each worker on its own: it ends those that pass and keeps the
// one running background work, live in the logbook.
func TestEndAllGatesEachWorker(t *testing.T) {
	defer worker.FastGate(time.Millisecond)()
	ctx := context.Background()
	r := newRig(t)
	_, _ = r.fleet.Spawn(ctx, "bees-8", "spawn and end")
	_, _ = r.fleet.Spawn(ctx, "bees-9", "second worker")
	r.h.child("bees-9", "go test ./...")
	ended, err := r.fleet.EndAll(ctx, worker.EndOpts{})
	if err != nil || len(ended) != 2 || ended[0].Err != nil || !errors.Is(ended[1].Err, worker.ErrBackground) {
		t.Fatalf("ended = %+v, %v; want bees-8 ended, bees-9 kept for background work", ended, err)
	}
	if sp, _, _ := r.lb.LatestSpawn(ctx, "bees-9"); sp.Ended {
		t.Fatal("bees-9 recorded ended")
	}
}

// bees-12.15: a worker resumes and ends on the harness its spawn recorded, whatever the
// config says now.
func TestAResumeUsesTheRecordedHarness(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.fleet.Harness, r.fleet.HarnessName = fakeHarness{}, "fake_harness"
	if _, err := r.fleet.Spawn(ctx, "bees-8", "spawn and end"); err != nil {
		t.Fatal(err)
	}
	r.h.exit("bees-8")
	r.fleet.Harness, r.fleet.HarnessName = claude.New(config.ClaudeCode{}), "claude_code" // the config switched
	asked := ""
	r.fleet.HarnessOf = func(name string) (harness.Harness, error) { asked = name; return fakeHarness{}, nil }
	if st, err := r.fleet.Spawn(ctx, "bees-8", ""); err != nil || !st.Resumed {
		t.Fatalf("resume = %+v, %v", st, err)
	}
	if asked != "fake_harness" || r.h.kind != "fake" {
		t.Fatalf("resumed on %q (kind %q), want the recorded fake_harness", asked, r.h.kind)
	}
	r.fleet.HarnessOf = nil
	if err := r.fleet.End(ctx, "bees-8", worker.EndOpts{Force: true}); err == nil || !strings.Contains(err.Error(), "spawned on harness fake_harness") {
		t.Fatalf("end with no adapter for the recorded harness: %v, want refused naming it", err)
	}
}
