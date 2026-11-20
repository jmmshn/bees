package scenario_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/cli"
	"github.com/jmmshn/bees/internal/fake"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/proc"
)

// A tag named like the base, on the branch's tip, proves nothing: the base is the branch
// refs/heads/main, never whatever "main" resolves to first (motif 3).
func TestATagNamedLikeTheBaseProvesNothing(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.git(x.repo, "tag", "main", "toy-1")
	must(t, x.queen("end", "toy-1"), 9, "toy-1 has work not on main")
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir+" (toy-1): toy-1 has work not on main")
}

// A base whose upstream is gone (fetch --prune after its PR merged) is measured against
// the base alone: never "can't check" forever (motif 5).
func TestAGoneUpstreamIsSkipped(t *testing.T) {
	x := newToy(t)
	x.git(x.repo, "config", "branch.main.remote", "origin")
	x.git(x.repo, "config", "branch.main.merge", "refs/heads/main")
	x.git(x.repo, "remote", "add", "origin", "/nonexistent/origin.git")
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	must(t, x.queen("end", "toy-1"), 9, "toy-1 has work not on main")
	x.git(x.repo, "merge", "-q", "toy-1")
	must(t, x.queen("end", "toy-1"), 0, "ended toy-1")
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir+" and branch toy-1")
}

// Commits made on a detached HEAD are on no branch: prune keeps the worktree that holds
// them, saying how to keep them.
func TestPruneKeepsCommitsOnADetachedHead(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "verdict")
	x.git(sp.Workspace.Dir, "checkout", "-q", "--detach")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done", "--answer", "yes", "--report", "fine"), 0)
	must(t, x.queen("end", "toy-1"), 0)
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir+" (toy-1): its HEAD has commits on no branch", "switch -c")
}

// A herdr pane working in an ended worker's directory (a keeper's shell, a pane moved
// out) keeps it: removing it would pull the floor from under that pane (the pilot's
// freeWorktree).
func TestPruneKeepsADirAPaneWorksIn(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.git(x.repo, "merge", "-q", "toy-1")
	must(t, x.queen("end", "toy-1"), 0)
	out, err := x.herdr.Run(context.Background(), "", "herdr", "--session", "toy", "workspace", "create", "--cwd", sp.Workspace.Dir, "--label", "keeper")
	if err != nil {
		t.Fatalf("workspace create: %v %s", err, out)
	}
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir+" (toy-1): herdr pane w2:p1 works in it")
}

// An ended worker whose recorded process still runs may still write in its directory:
// prune keeps it until that process stops.
func TestPruneKeepsAnEndedWorkerThatStillRuns(t *testing.T) {
	x := newToy(t)
	ctx := context.Background()
	x.herdr.Programs["claude"] = "/bin/true" // the start is never confirmed
	x.ports.Timing.Confirm = 300 * time.Millisecond
	x.bead("toy-1", "x")
	must(t, x.queen("spawn", "--label", "x", "--goal", "verdict", "toy-1"), 8)
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	// It started after all, late: a process bs recorded and couldn't stop.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	start, err := proc.Start(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	lb, err := logbook.Open(ctx, x.logbook)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	sp, _, err := lb.LatestSpawn(ctx, "toy-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := lb.RecordStartOnce(ctx, logbook.Start{Spawn: sp.ID, Name: sp.Name, Process: proc.Process{PID: cmd.Process.Pid, Start: start}, Harness: "claude"}); err != nil {
		t.Fatal(err)
	}
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir+" (toy-1): its ended worker still runs (pid "+strconv.Itoa(cmd.Process.Pid)+")")
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait() // killed: its error is the signal
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir+" and branch toy-1")
}

// A spawn ended --incomplete with nothing handed off can never complete: status points
// at the keeper's discard, not at a prune that would never complete it.
func TestStatusPointsAnUnhandedGoalAtDiscard(t *testing.T) {
	x := newToy(t)
	x.herdr.Programs["claude"] = "/bin/true"
	x.ports.Timing.Confirm = 300 * time.Millisecond
	x.bead("toy-1", "x")
	must(t, x.queen("spawn", "--label", "x", "--goal", "branch", "toy-1"), 8)
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	x.herdr.Programs["claude"] = fake.Beesfake(t)
	must(t, x.queen("prune"), 0)
	// Its workspace gone too, the stuck line alone keeps its row (bees-13.78.17).
	if err := os.RemoveAll(filepath.Join(filepath.Dir(x.repo), "toy-WT", "toy-1")); err != nil {
		t.Fatal(err)
	}
	r := x.keeper("status")
	must(t, r, 0, "stuck: toy-1: ended with nothing handed off, so its goal can't complete → the keeper discards it (bs goal toy-1 discard, bees-13.6)")
	if cell(r.out, "toy-1", "WORKSPACE") != "toy-1 (removed)" || strings.Contains(r.out, "hidden:") { // stuck is never hidden (bees-13.78.17)
		t.Fatalf("the stuck row:\n%s", r)
	}
}

// An origin that isn't on github.com (an ssh alias, GitHub Enterprise) is never asked for
// merged pull requests, and the reason says so rather than only "has work not on main".
func TestANonGitHubOriginSaysNoPullRequestWasLookedFor(t *testing.T) {
	x := newToy(t)
	x.git(x.repo, "remote", "add", "origin", "git@gh-work:x/toy.git")
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	must(t, x.queen("end", "toy-1"), 9, "toy-1 has work not on main (origin git@gh-work:x/toy.git is not on github.com, so no pull request was looked for)")
}

// A worktree git refuses to remove (locked) is kept, naming the way out (exit 3), never
// "couldn't check" (exit 4) on every prune.
func TestPruneKeepsALockedWorktree(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.git(x.repo, "merge", "-q", "toy-1")
	must(t, x.queen("end", "toy-1"), 0)
	x.git(x.repo, "worktree", "lock", sp.Workspace.Dir)
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir+" (toy-1): git refused to remove it", "git worktree unlock")
	x.git(x.repo, "worktree", "unlock", sp.Workspace.Dir)
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir+" and branch toy-1")
}

// trustRec is the fake harness as one that starts only in a folder it trusts (agy):
// it records each folder trusted, and refuses while fail is set.
type trustRec struct {
	fake.Harness
	mu      sync.Mutex
	trusted []string
	fail    error
}

func (h *trustRec) Trust(dir string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fail != nil {
		return h.fail
	}
	if !slices.Contains(h.trusted, dir) {
		h.trusted = append(h.trusted, dir)
	}
	return nil
}

func (h *trustRec) Untrust(dir string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.trusted = slices.DeleteFunc(h.trusted, func(d string) bool { return d == dir })
	return nil
}

func (h *trustRec) all() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.trusted)
}

// bees-13.13.5: a harness that starts only in a folder it trusts is told to trust the
// worktree a spawn made, and nothing else; prune takes the trust back with the worktree.
// A trust that fails takes back the worktree, and records nothing (motif 8).
func TestSpawnTrustsItsWorktreeAndPruneUntrustsIt(t *testing.T) {
	x := newToy(t)
	h := &trustRec{Harness: x.harness, fail: errors.New("settings.json can't be read")}
	x.ports.Harness = h
	x.bead("toy-1", "work on toy-1")
	must(t, x.queen("spawn", "--goal", "branch", "--label", "toy 1", "toy-1"), 1, "couldn't trust its folder", "settings.json can't be read", "nothing was recorded")
	if x.hasBranch("toy-1") || len(h.all()) > 0 {
		t.Fatalf("a refused trust left a branch (%v) or trust %v", x.hasBranch("toy-1"), h.all())
	}
	h.fail = nil
	sp := x.spawn("toy-1", "branch")
	if got := h.all(); !slices.Equal(got, []string{sp.Workspace.Dir}) {
		t.Fatalf("trusted after spawn: %v", got)
	}
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.git(x.repo, "merge", "-q", "toy-1")
	must(t, x.queen("end", "toy-1"), 0, "ended toy-1")
	if got := h.all(); len(got) != 1 {
		t.Fatalf("bs end took trust back before prune: %v", got)
	}
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir)
	if got := h.all(); len(got) > 0 {
		t.Fatalf("trusted after prune: %v", got)
	}
}

// agy.settings, ~ expanded, reaches the adapter bs builds (bees-14.1 wired by bees-13.13.5).
func TestAgySettingsReachTheAdapter(t *testing.T) {
	x := newToy(t)
	b, err := os.ReadFile(filepath.Join(x.config, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	x.write(filepath.Join(x.config, "config.toml"), "[agy]\nsettings = \"~/agy/settings.json\"\n"+string(b))
	var mu sync.Mutex
	var got []string
	x.ports.Harness = nil
	x.ports.NewHarness = func(name string, o cli.HarnessOpts) port.Harness {
		mu.Lock()
		got = append(got, o.Settings)
		mu.Unlock()
		return x.harness
	}
	x.spawn("toy-1", "branch")
	mu.Lock()
	defer mu.Unlock()
	home := filepath.Join(filepath.Dir(x.repo), "home")
	if len(got) == 0 || got[0] != filepath.Join(home, "agy", "settings.json") {
		t.Fatalf("settings the adapter got: %v", got)
	}
}

// inDir starts sleep working in dir, detached as a worker's leaked background task is:
// reparented away from anything bs or the test started, with env added to a copy of the
// test's own without any HERDR_* (bees-13.31).
func inDir(t *testing.T, dir string, env ...string) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 30 </dev/null >/dev/null 2>&1 & echo $!")
	cmd.Dir = dir
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "HERDR_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	for range 50 { // until sh's fork has exec'd sleep
		if c, err := proc.Command(pid); err == nil && strings.HasPrefix(c, "sleep") {
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d never ran sleep", pid)
	return 0
}

// running reports whether pid still runs (a zombie doesn't).
func running(t *testing.T, pid int) bool {
	t.Helper()
	_, err := proc.Start(pid)
	if err != nil && !errors.Is(err, proc.ErrGone) {
		t.Fatal(err)
	}
	return err == nil
}

// A process working in an ended worker's directory, whatever its parent, keeps it: prune
// names it, and removes the directory once it stops (bees-13.31).
func TestPruneKeepsADirAProcessWorksIn(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.git(x.repo, "merge", "-q", "toy-1")
	sub := filepath.Join(sp.Workspace.Dir, "sub") // below it counts too; empty, git sees nothing
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := inDir(t, sub)
	p := strconv.Itoa(pid)
	// No child of its harness: end leaves it running, and says so.
	must(t, x.queen("end", "toy-1"), 0, "still working in toy-1's workspace", "so left running", "pid "+p+" ", "sleep 30")
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir+" (toy-1): processes still work in it", "pid "+p+" ", "sleep 30")
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	for running(t, pid) {
		time.Sleep(10 * time.Millisecond)
	}
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir)
}

// unconfirmed spawns toy-1 under a harness that exits at once: no start is ever
// confirmed, so the gate has no process to walk.
func unconfirmed(t *testing.T, x *toy) logbook.Spawn {
	t.Helper()
	x.herdr.Programs["claude"] = "/bin/true"
	x.ports.Timing.Confirm = 300 * time.Millisecond
	x.bead("toy-1", "x")
	must(t, x.queen("spawn", "--label", "x", "--goal", "verdict", "toy-1"), 8)
	ctx := context.Background()
	lb, err := logbook.Open(ctx, x.logbook)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	sp, _, err := lb.LatestSpawn(ctx, "toy-1")
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

// With no harness process to walk, the gate looks at what works in the workspace: bs end
// refuses while a process does, and --force stops only what was started from the worker's
// pane, recording it; a process from elsewhere (a keeper's shell, a stray) is named and
// left running (bees-13.31, 13.16, motif 12). Which pane a process came from is read from
// its environment, which bs can read only on Linux.
func TestEndForceStopsOnlyWhatItsPaneStarted(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ties by environment only where /proc has it")
	}
	x := newToy(t)
	sp := unconfirmed(t, x)
	mine := inDir(t, sp.Workspace.Dir, "HERDR_PANE_ID="+sp.Herdr.Pane)
	other := inDir(t, sp.Workspace.Dir, "HERDR_PANE_ID=w9:p9")
	m, o := strconv.Itoa(mine), strconv.Itoa(other)
	must(t, x.queen("end", "--incomplete", "toy-1"), 5, "processes still work in "+sp.Workspace.Dir, "pid "+m+" ", "pid "+o+" ",
		"sleep 30   (not started from its pane)", "next: bs end --incomplete --force toy-1")
	r := x.queen("end", "--incomplete", "--force", "toy-1")
	must(t, r, 0, "stopped what still worked in toy-1's workspace", "--force stops only what was started from its pane, so left running")
	out := r.String()
	at := func(sub string) int { return strings.Index(out, sub) }
	if !slices.IsSorted([]int{at("stopped what"), at("pid " + m + " "), at("so left running"), at("pid " + o + " ")}) {
		t.Fatalf("want pid %s stopped and pid %s left:\n%s", m, o, r)
	}
	for running(t, mine) {
		time.Sleep(10 * time.Millisecond) // reaped by init
	}
	if !running(t, other) {
		t.Fatalf("pid %s, not started from its pane, was stopped", o)
	}
	ctx := context.Background()
	lb, err := logbook.Open(ctx, x.logbook)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	e, ok, err := lb.EndOf(ctx, sp.ID)
	if err != nil || !ok || len(e.Stopped) != 1 || e.Stopped[0].PID != mine {
		t.Fatalf("ended = %+v, %v, %v; want only pid %s in Stopped", e, ok, err, m)
	}
}

// A process table bs can't list fails end and prune closed: never "nothing works there".
func TestEndAndPruneFailClosedWhenTheyCantListWhatWorksThere(t *testing.T) {
	x := newToy(t)
	unconfirmed(t, x)
	x.herdr.FailWalks(errors.New("proc unreadable"))
	must(t, x.queen("end", "--incomplete", "toy-1"), 6, "toy-1: can't tell", "proc unreadable", "next: bs end --incomplete --force toy-1")
	x.herdr.FailWalks(nil)
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	must(t, x.keeper("goal", "toy-1", "discard"), 0)
	x.herdr.FailWalks(errors.New("proc unreadable"))
	must(t, x.queen("prune"), 4, "can't tell what uses it: proc unreadable")
}
