package scenario_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/cli"
	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/fake"
	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/proc"
)

// Sessions (bees-13.4): pane identity across logbooks, liveness, the queen, start
// confirmation and its heal, mail and rings.

// shell opens a workspace on session's server at dir and starts a beesfake there as a
// keeper's shell, named name: an unrecorded herdr pane whose script runs bs calls.
func (x *toy) shell(session, name, dir string) herdr.Workspace {
	x.t.Helper()
	h := herdr.Client{Run: x.herdr, Session: session}
	w, err := h.CreateWorkspace(context.Background(), dir, name)
	if err != nil {
		x.t.Fatal(err)
	}
	if err := h.StartAgent(context.Background(), w.Pane, name, "claude", "--name", name, "--state", x.harness.Dir(name)); err != nil {
		x.t.Fatal(err)
	}
	return w
}

// startQueen runs bs queen in a keeper's shell pane: it records queen-toy and execs
// beesfake as her harness, keeping the pid it recorded. It returns her queen entry.
func (x *toy) startQueen() logbook.Queen {
	x.t.Helper()
	x.shell(x.session, "keeper", x.repo)
	x.harness.Step(x.t, "keeper", "bs&", "queen")
	x.harness.Await(x.t, "keeper", 1)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(x.harness.Dir("queen-toy"), "ring.sock")); err == nil {
			q, ok, err := x.open().LatestQueen(context.Background(), "queen-toy")
			if err != nil || !ok {
				x.t.Fatalf("no queen entry: %v", err)
			}
			return q
		}
	}
	x.t.Fatal("queen-toy's harness never came up")
	return logbook.Queen{}
}

// queenRuns runs bs args as queen-toy, in her pane: the nth step of her script.
func (x *toy) queenRuns(n int, args ...string) result { return x.worker("queen-toy", n, args...) }

// bs queen records her pane and her process, the one exec keeps, then her bs calls find
// her by that pane; a second queen is refused while she runs, and so is one outside a
// pane.
func TestTheQueenIsFoundByHerPane(t *testing.T) {
	x := newToy(t)
	must(t, x.keeper("queen"), 2, "not in a herdr pane")
	q := x.startQueen()
	if q.Session != "toy" || q.Pane != "w1:p1" || q.Harness != "claude_code" {
		t.Fatalf("queen entry %+v", q)
	}
	if alive, err := x.herdr.Alive(q.Process); err != nil || !alive {
		t.Fatalf("her recorded process isn't alive: %v", err)
	}
	r := x.queenRuns(1, "status")
	must(t, r, 0)
	if cell(r.out, "queen-toy", "RUNNING") != "yes" {
		t.Fatalf("status:\n%s", r)
	}
	files, _ := filepath.Glob(filepath.Join(x.ports.CacheDir, "queen-toy-*"))
	x.shell(x.session, "keeper2", x.repo)
	must(t, x.worker("keeper2", 1, "queen"), 2, "queen-toy already runs", "next: use her")
	if again, _ := filepath.Glob(filepath.Join(x.ports.CacheDir, "queen-toy-*")); len(files) == 0 || !slices.Equal(files, again) {
		t.Fatalf("a refused second queen touched the first's files: %q, then %q", files, again)
	}
	// A BS_LOGBOOK_DIR other than paths.logbook_dir, inherited by a shell: bs queen would
	// record her where her own calls never look (motif 14).
	x.herdr.Env = append(x.herdr.Env, "BS_LOGBOOK_DIR="+t.TempDir())
	x.shell(x.session, "keeper3", x.repo)
	must(t, x.worker("keeper3", 1, "queen"), 2, "isn't paths.logbook_dir", "nothing was recorded")
}

// Mail both ways between the queen and a worker she spawned from her pane: each send is
// stored, then rung at the recorded process; the doorbell says who from and how to reply.
func TestQueenAndWorkerMailBothWays(t *testing.T) {
	x := newToy(t)
	x.startQueen()
	x.bead("toy-1", "work on toy-1")
	must(t, x.queenRuns(1, "spawn", "--label", "x", "--goal", "branch", "toy-1"), 0, "spawned toy-1")
	must(t, x.worker("toy-1", 1, "inbox"), 0, "from queen-toy", "You own toy-1")
	must(t, x.worker("toy-1", 2, "send", "queen-toy", "which base?"), 0, "rung queen-toy")
	if rings := x.harness.Rings("queen-toy"); len(rings) != 1 || !strings.Contains(rings[0], `1 unread (toy-1) — run: bs inbox   (reply with: bs send toy-1 "...")`) {
		t.Fatalf("queen's rings: %q", rings)
	}
	must(t, x.queenRuns(2, "inbox"), 0, "from toy-1", "which base?")
	must(t, x.queenRuns(3, "send", "--now", "toy-1", "main"), 0, "rung toy-1")
	if rings := x.harness.Rings("toy-1"); len(rings) == 0 || !strings.HasPrefix(rings[len(rings)-1], "now\t") {
		t.Fatalf("toy-1's rings: %q, want the last at priority now", rings)
	}
	must(t, x.worker("toy-1", 3, "inbox"), 0, "from queen-toy", "main")
}

// bs send - reads the message from stdin.
func TestSendReadsAMessageFromStdin(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	must(t, x.shellRun(x.repo, x.env, "line one\nline two\n", "send", "--as", "queen-toy", "toy-1", "-"), 0, "rung toy-1")
	must(t, x.worker("toy-1", 1, "inbox"), 0, "line one\nline two\n")
}

// bees-13.14, bees-13.49: a keeper's herdr shell is a pane no entry recorded, in no
// worker's workspace: bs runs there as the keeper, and --as names a session as outside
// herdr.
func TestAnUnrecordedPaneIsTheKeeper(t *testing.T) {
	x := newToy(t)
	x.start()
	x.shell(x.session, "keeper", x.repo)
	must(t, x.worker("keeper", 1, "send", "queen-toy", "hi"), 0, "to queen-toy")
	must(t, x.worker("keeper", 2, "inbox"), 0, "no unread messages")
	must(t, x.worker("keeper", 3, "send", "--as", "keeper", "queen-toy", "again"), 0, "to queen-toy")
	must(t, x.keeper("inbox", "--as", "queen-toy"), 0, "from keeper", "hi", "again")
}

// ADR-0010 Amendment 4 (OQ-K6): a call acts in the repo it runs in. Outside every repo a
// session acts in its own project; inside another project's repo it acts there, as a
// session of its own project (here other has no logbook, so status is refused).
func TestASessionActsInTheRepoItRunsIn(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	other := filepath.Join(filepath.Dir(x.repo), "other")
	x.git(filepath.Dir(x.repo), "init", "-q", other)
	x.harness.Step(t, "toy-1", "cd", os.TempDir())
	must(t, x.worker("toy-1", 2, "status"), 0, "as toy-1 (worker of toy")
	x.harness.Step(t, "toy-1", "cd", other)
	must(t, x.worker("toy-1", 4, "status"), 2, "no logbook for project other")
	x.harness.Step(t, "toy-1", "cd", os.TempDir())
	must(t, x.worker("toy-1", 6, "inbox"), 0, "You own toy-1")
	// The queen too: her entry records the repo she started in, so a spawn from anywhere
	// still has one.
	x.startQueen()
	x.bead("toy-2", "work on toy-2")
	x.harness.Step(t, "queen-toy", "cd", os.TempDir())
	must(t, x.queenRuns(2, "spawn", "--label", "x", "--goal", "verdict", "toy-2"), 0, "spawned toy-2")
}

// bees-12.43 across logbooks: the pane scan reads every <project>.db read-only, skips a
// .db.pre-N copy, and fails closed on a logbook it can't read.
func TestThePaneScanReadsEveryLogbook(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	dir := filepath.Dir(x.logbook)
	lb, _, err := logbook.Create(context.Background(), filepath.Join(dir, "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	lb.Close()
	x.write(filepath.Join(dir, "toy.db.pre-1"), "not a logbook")
	must(t, x.keeper("status"), 0)
	x.write(filepath.Join(dir, "broken.db"), "not a logbook")
	// A worker is found in its own project's logbook first (P-first); the keeper's call,
	// found in none, reads every one and fails closed.
	must(t, x.worker("toy-1", 1, "inbox"), 0, "You own toy-1")
	must(t, x.keeper("status"), 6, "broken.db")
}

// A pane on a second herdr session with the same pane id as a recorded one names no one
// there: the session is part of the key (ADR-0010).
func TestTheSamePaneIDOnAnotherSessionNamesNoOne(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "verdict")
	two := filepath.Join(filepath.Dir(x.config), "toy2")
	if err := os.MkdirAll(two, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(x.config, "config.toml"))
	x.write(filepath.Join(two, "config.toml"), string(b))
	w := x.shell("toy2", "elsewhere", x.repo)
	if w.Pane != sp.Herdr.Pane {
		t.Fatalf("toy2's first pane is %s, not %s: herdr numbers each server's panes from w1:p1", w.Pane, sp.Herdr.Pane)
	}
	must(t, x.worker("elsewhere", 1, "inbox"), 0, "no unread messages") // the keeper's
	must(t, x.worker("elsewhere", 2, "inbox", "--as", "toy-1"), 0, "You own toy-1")
}

// A worker whose pane moved keeps running, is rung at its recorded process, and its own
// bs calls still find it by the pane its env names.
func TestRound11AMovedPaneStaysRunningAndIsRung(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "verdict")
	if _, err := x.herdr.Run(context.Background(), "", "herdr", "--session", "toy", "pane", "move", sp.Herdr.Pane, "--new-workspace"); err != nil {
		t.Fatal(err)
	}
	if r := x.keeper("status"); cell(r.out, "toy-1", "RUNNING") != "yes" {
		t.Fatalf("status:\n%s", r)
	}
	must(t, x.queen("send", "toy-1", "still there?"), 0, "rung toy-1")
	must(t, x.worker("toy-1", 1, "inbox"), 0, "still there?")
}

// A start confirmation missed (the process table couldn't be read while it looked) is
// healed on a later trigger: recorded late, and status says so. A heal whose probe fails
// (herdr unreachable) prints and writes nothing.
func TestAMissedStartIsHealedLate(t *testing.T) {
	x := newToy(t)
	x.ports.Timing.Confirm, x.ports.Timing.AfterErr = 300*time.Millisecond, 300*time.Millisecond
	x.bead("toy-1", "work on toy-1")
	x.herdr.FailStarts(errors.New("ps: can't read the table"))
	must(t, x.queen("spawn", "--label", "x", "--goal", "verdict", "toy-1"), 6, "can't look for its harness", "can't read the table")
	x.herdr.FailStarts(nil)
	sp, _, _ := x.open().LatestSpawn(context.Background(), "toy-1")
	x.herdr.FailInfo(1000)
	r := x.keeper("status")
	if !strings.HasPrefix(cell(r.out, "toy-1", "RUNNING"), "unknown: can't look for toy-1's harness") {
		t.Fatalf("status with herdr down:\n%s", r)
	}
	if _, ok, _ := x.open().LatestStart(context.Background(), sp.ID); ok {
		t.Fatal("a failed heal probe wrote a start")
	}
	x.herdr.FailInfo(0)
	r = x.keeper("status")
	must(t, r, 0, "toy-1's start was never confirmed; found it running", "late: toy-1: its start was recorded late, after confirmation missed it")
	if cell(r.out, "toy-1", "RUNNING") != "yes (late)" {
		t.Fatalf("status:\n%s", r)
	}
	if st, ok, _ := x.open().LatestStart(context.Background(), sp.ID); !ok || !st.Late {
		t.Fatalf("start %+v %v, want late", st, ok)
	}
}

// The worker's own first bs call heals its start too: identity finds its harness, with
// --name toy-1 in its argv, above the call.
func TestAWorkersFirstCallHealsItsStart(t *testing.T) {
	x := newToy(t)
	x.ports.Timing.Confirm, x.ports.Timing.AfterErr = 300*time.Millisecond, 300*time.Millisecond
	x.bead("toy-1", "work on toy-1")
	x.herdr.FailStarts(errors.New("ps: can't read the table"))
	must(t, x.queen("spawn", "--label", "x", "--goal", "verdict", "toy-1"), 6)
	x.herdr.FailStarts(nil)
	must(t, x.worker("toy-1", 1, "inbox"), 0, "You own toy-1", "recorded late")
}

// A start healed late has no baseline: work begun before the heal can't be told from its
// own, so the gate fails closed (exit 6) and names --force, rather than pass it.
func TestAStartHealedLateFailsTheGateClosed(t *testing.T) {
	x := newToy(t)
	x.ports.Timing.Confirm, x.ports.Timing.AfterErr = 300*time.Millisecond, 300*time.Millisecond
	x.bead("toy-1", "work on toy-1")
	x.herdr.FailStarts(errors.New("ps: can't read the table"))
	must(t, x.queen("spawn", "--label", "x", "--goal", "verdict", "toy-1"), 6)
	x.herdr.FailStarts(nil)
	x.harness.Step(t, "toy-1", "listen", "server")
	x.harness.Await(t, "toy-1", 1)
	must(t, x.worker("toy-1", 2, "done", "--answer", "yes", "--report", "ok"), 6, "recorded late", "it began with are unknown", "next: bs done --force")
	must(t, x.worker("toy-1", 3, "done", "--force", "--answer", "yes", "--report", "ok"), 0)
	must(t, x.keeper("end", "--as", "queen-toy", "toy-1"), 6, "it began with are unknown", "next: bs end --force toy-1")
}

// bees-12.38: a message for another project's session, whose logbook is in the logbook
// dir, is refused with nothing stored; --project writes it there, recording the sender's
// project for the reply (T11).
func TestAMisaddressedSendIsRefused(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	path := filepath.Join(filepath.Dir(x.logbook), "other.db")
	lb, _, err := logbook.Create(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	lb.Close()
	must(t, x.worker("toy-1", 1, "send", "queen-other", "hi"), 2, "queen-other is a session of project other, not toy; nothing stored", "bs send --project other queen-other")
	if ms, _ := x.open().Unread(context.Background(), "queen-other"); len(ms) != 0 {
		t.Fatalf("stored: %+v", ms)
	}
	must(t, x.worker("toy-1", 2, "send", "--project", "other", "queen-other", "hi"), 0, "stored, not rung (queen-other: not running): queen-other reads it at its next bs inbox")
	ro, err := logbook.OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	ms, err := ro.Unread(context.Background(), "queen-other")
	if err != nil || len(ms) != 1 || ms[0].From != "toy-1" || ms[0].FromProject != "toy" {
		t.Fatalf("other's inbox: %+v %v", ms, err)
	}
	must(t, x.worker("toy-1", 3, "send", "stranger", "hi"), 0, "stored, not rung")
}

// T11 (round 11's cross-project send and reply): a worker's send --project lands in the
// other project's logbook, and that project's inbox shows how to reply across: queen to
// queen (T2).
func TestRound11CrossProjectSendAndReply(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	lb, _, err := logbook.Create(context.Background(), filepath.Join(filepath.Dir(x.logbook), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	lb.Close()
	must(t, x.worker("toy-1", 1, "send", "--project", "other", "queen-other", "your API changed?"), 0)
	other := append(slices.Clone(x.env), "BS_PROJECT=other")
	must(t, x.shellRun("/", other, "", "inbox", "--as", "queen-other"), 0,
		"from toy-1", "your API changed?", `(reply with: bs send --project toy queen-toy "...")`)
}

// worker.path reaches the harness as dirs first on the worker's PATH (the live rig's
// build under test).
func TestRound11WorkersRunTheBuildUnderTest(t *testing.T) {
	x := newToy(t)
	bin := t.TempDir()
	if err := os.MkdirAll(filepath.Join(x.config, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	x.write(filepath.Join(x.config, "projects", "toy.toml"), "[worker]\npath = [\""+bin+"\"]\n")
	if sp := x.spawn("toy-1", "verdict"); sp.Record["path"] != bin {
		t.Fatalf("recorded launch: %v, want path %s", sp.Record, bin)
	}
}

// bees-12.50: a reply-wait loop left running with & is background work: the gate lists
// it with any port it listens on, and says a reply-wait loop counts.
func TestPilotBug1250AReplyWaitLoopInTheBackground(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	x.harness.Step(t, "toy-1", "listen", "server")
	x.harness.Await(t, "toy-1", 1) // the step returns once its port is bound
	must(t, x.worker("toy-1", 2, "done"), 5, "still runs background work", "listening :", "a reply-wait loop too")
}

// move moves pane on the toy's server: into workspace ws, or a new one when ws is "".
func (x *toy) move(pane, ws string) {
	x.t.Helper()
	args := []string{"herdr", "--session", x.session, "pane", "move", pane, "--new-workspace"}
	if ws != "" {
		args = []string{"herdr", "--session", x.session, "pane", "move", pane, "--new-tab", "--workspace", ws}
	}
	if out, err := x.herdr.Run(context.Background(), "", args...); err != nil {
		x.t.Fatalf("pane move: %v %s", err, out)
	}
}

// bees-12.36: bs end refuses to close a workspace another worker's pane was moved into,
// naming it, and closes nothing.
func TestRound11EndRefusesAWorkspaceHoldingAnotherWorkersPane(t *testing.T) {
	x := newToy(t)
	sp1 := x.spawn("toy-1", "verdict")
	sp2 := x.spawn("toy-2", "verdict")
	x.move(sp2.Herdr.Pane, sp1.Herdr.Workspace)
	must(t, x.worker("toy-1", 1, "done", "--answer", "yes", "--report", "fine"), 0)
	must(t, x.queen("end", "toy-1"), 2, "also holds toy-2 (pid", "nothing closed", "pane move")
	if _, ok := x.herdr.Agent("toy", "toy-2"); !ok {
		t.Fatal("toy-2 was ended")
	}
}

// bees-12.46: bs end refuses a worker whose pane moved out of its workspace, since
// closing the workspace wouldn't stop it; --force stops it by its recorded process.
func TestRound11EndOnAMovedWorker(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "verdict")
	must(t, x.worker("toy-1", 1, "done", "--answer", "yes", "--report", "fine"), 0)
	x.move(sp.Herdr.Pane, "")
	if fs, _ := filepath.Glob(filepath.Join(x.ports.CacheDir, "toy-1-*")); len(fs) == 0 {
		t.Fatal("no start files to remove")
	}
	must(t, x.queen("end", "toy-1"), 2, "moved out of its workspace", "nothing closed", "bs end --force toy-1")
	if _, ok := x.herdr.Agent("toy", "toy-1"); !ok {
		t.Fatal("a refused end stopped toy-1")
	}
	if _, ok, _ := x.open().EndOf(context.Background(), sp.ID); ok {
		t.Fatal("a refused end was recorded")
	}
	must(t, x.queen("end", "--force", "toy-1"), 0, "ended toy-1")
	if _, ok := x.herdr.Agent("toy", "toy-1"); ok {
		t.Fatal("toy-1 still runs")
	}
	if fs, _ := filepath.Glob(filepath.Join(x.ports.CacheDir, "toy-1-*")); len(fs) != 0 {
		t.Fatalf("bs end left start files: %q", fs)
	}
	// Mail for an ended worker would never be read: refused, nothing stored (motif 11).
	must(t, x.queen("send", "toy-1", "one more thing"), 2, "toy-1 has ended", "nothing stored")
}

// Motif 14: a BS_LOGBOOK_DIR pin other than paths.logbook_dir would record a pane where
// its own bs calls never look, so bs spawn (and bs queen) refuse to record under one.
func TestRecordingAPaneUnderABSLogbookDirPinIsRefused(t *testing.T) {
	x := newToy(t)
	x.bead("toy-1", "work on toy-1")
	env := x.env
	x.env = append(slices.Clone(env), "BS_LOGBOOK_DIR="+t.TempDir())
	must(t, x.queen("spawn", "--goal", "verdict", "toy-1"), 2, "isn't paths.logbook_dir", "nothing was recorded", "env -u BS_LOGBOOK_DIR")
	x.env = append(slices.Clone(env), "BS_LOGBOOK_DIR="+filepath.Dir(x.logbook)) // the config's own dir: no harm
	must(t, x.queen("spawn", "--label", "x", "--goal", "verdict", "toy-1"), 0, "spawned toy-1")
}

// kill stops worker name's harness process and waits for herdr to see it gone.
func (x *toy) kill(name string) {
	x.t.Helper()
	pid, ok := x.herdr.Agent(x.session, name)
	if !ok {
		x.t.Fatalf("no agent %s", name)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		x.t.Fatal(err)
	}
	for range 100 {
		if _, ok := x.herdr.Agent(x.session, name); !ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	x.t.Fatalf("%s still runs", name)
}

// bees-13.27: bs spawn on an open spawn whose process died restarts it in its recorded
// pane and records a new start that names the dead one, so status, rings and the gate
// follow the restart. A live worker, or one
// whose pane is gone, is refused, saying the next step (motif 5, 9, 10).
func TestSpawnResumesADeadWorker(t *testing.T) {
	x := newToy(t)
	rec := &startsRec{Harness: x.harness} // installed before any relay reads the ports
	x.ports.Harness = rec
	sp := x.spawn("toy-1", "branch")
	must(t, x.worker("toy-1", 1, "inbox"), 0)
	first, _, _ := x.open().LatestStart(context.Background(), sp.ID)
	must(t, x.queen("spawn", "--goal", "branch", "toy-1"), 2, "toy-1 is already running", "next: bs send toy-1")
	x.kill("toy-1")
	must(t, x.keeper("status"), 0, "stuck: toy-1: its process is gone but it isn't ended → bs spawn --goal branch toy-1 resumes it in its pane")
	must(t, x.queen("send", "toy-1", "while you were down"), 0, "stored, not rung")
	must(t, x.queen("spawn", "--goal", "branch", "--label", "other work", "toy-1"), 2, `labelled "toy 1", not "other work"`, "leave --label off")
	must(t, x.queen("spawn", "--goal", "branch", "toy-1"), 0, "resumed toy-1 in pane "+sp.Herdr.Pane)
	// The resume prepares the same start a spawn does, in its own conversation (it read its inbox).
	if st := rec.all(); len(st) != 2 || st[1].ShellTimeout != core.ShellTimeout || !st[1].Continue || st[1].Deny == nil || st[0].Continue {
		t.Fatalf("the spawn's and the resume's starts: %+v", st)
	}
	st, ok, err := x.open().LatestStart(context.Background(), sp.ID)
	if err != nil || !ok || st.ID == first.ID || st.Replaces != first.ID || st.PID == first.PID {
		t.Fatalf("the resume's start: %+v (first %+v), %v %v", st, first, ok, err)
	}
	if r := x.keeper("status"); cell(r.out, "toy-1", "RUNNING") != "yes" {
		t.Fatalf("status after the resume:\n%s", r)
	}
	// Its doorbell says to run bs inbox, where the mail it missed waits; a send now rings it.
	must(t, x.queen("send", "toy-1", "welcome back"), 0, "rung toy-1")
	var rings []string
	for range 100 { // beesfake logs a ring as it takes it
		if rings = x.harness.Rings("toy-1"); len(rings) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The resumed process is rung; how many it has unread depends on whether its fake replayed
	// its script's first inbox on restart, which can read the send before its ring is
	// counted ("0 unread ()", seen on CI): only the ring itself is asserted.
	if len(rings) == 0 || !strings.Contains(rings[len(rings)-1], "run: bs inbox") {
		t.Fatalf("the resumed worker's rings: %q", rings)
	}
	must(t, x.queen("spawn", "--goal", "verdict", "toy-1"), 2, "it was spawned with --goal branch", "bs goal toy-1")

	sp2 := x.spawn("toy-2", "branch")
	x.kill("toy-2")
	if _, err := x.herdr.Run(context.Background(), "", "herdr", "--session", x.session, "workspace", "close", sp2.Herdr.Workspace); err != nil {
		t.Fatal(err)
	}
	must(t, x.queen("spawn", "--goal", "branch", "toy-2"), 2, "pane "+sp2.Herdr.Pane+" is gone", "bs end --incomplete toy-2")
}

// A bead whose last spawn ended incomplete, its worktree and branch kept, spawns again on
// them, never "branch already exists" with no way on (motif 5): the work so far is kept
// and counts. Every next: on the way is accepted: a lost pane's spawn says to end it
// --incomplete and spawn again, and both run.
func TestRespawnReusesAKeptWorkspace(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "half.txt")
	x.kill("toy-1")
	if _, err := x.herdr.Run(context.Background(), "", "herdr", "--session", x.session, "workspace", "close", sp.Herdr.Workspace); err != nil {
		t.Fatal(err)
	}
	r := x.queen("spawn", "--goal", "branch", "toy-1")
	must(t, r, 2, "next: bs end --incomplete toy-1, then bs spawn")
	must(t, x.next(r), 0, "ended toy-1") // the literal next: text, as the queen would type it
	must(t, x.queen("spawn", "--goal", "branch", "toy-1"), 0, "spawned toy-1", "on its kept worktree and branch toy-1")
	sp2, _, err := x.open().LatestSpawn(context.Background(), "toy-1")
	if err != nil || sp2.ID == sp.ID || sp2.Label != sp.Label || sp2.Workspace.Dir != sp.Workspace.Dir || sp2.Workspace.Branch != "toy-1" || sp2.Workspace.Base != sp.Workspace.Base {
		t.Fatalf("the second spawn: %+v (first %+v), %v", sp2, sp, err)
	}
	if _, err := os.Stat(filepath.Join(sp.Workspace.Dir, "half.txt")); err != nil {
		t.Fatalf("the kept work is gone: %v", err)
	}
	must(t, x.worker("toy-1", 1, "done"), 0, "branch_ahead: 1 commit ahead of main")
}

// A worker restarted in its pane by hand (claude --resume, not bs spawn) runs with --name
// <worker>: bs treats its dead recorded start like none and heals the new one from the
// pane, so status shows it running, it is rung, and its own bs calls know it (bees-13.9,
// A2; bakeoff S11).
func TestAWorkerRestartedByHandIsHealed(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	first, _, _ := x.open().LatestStart(context.Background(), sp.ID)
	x.kill("toy-1")
	// The hand restart: the same harness argv in the same pane, with no bs in between.
	if _, err := x.herdr.Run(context.Background(), "", "herdr", "--session", x.session, "agent", "start", "toy-1", "--kind", "claude", "--pane", sp.Herdr.Pane,
		"--timeout", "5000", "--", "--name", "toy-1", "--state", x.harness.Dir("toy-1")); err != nil {
		t.Fatal(err)
	}
	r := x.keeper("status")
	if !strings.HasPrefix(cell(r.out, "toy-1", "RUNNING"), "yes") || !strings.Contains(r.String(), "started outside bs: recorded late") {
		t.Fatalf("status after a hand restart:\n%s", r)
	}
	st, ok, err := x.open().LatestStart(context.Background(), sp.ID)
	if err != nil || !ok || st.Replaces != first.ID || !st.Late {
		t.Fatalf("the healed start: %+v (first %s), %v %v", st, first.ID, ok, err)
	}
	must(t, x.queen("send", "toy-1", "still there?"), 0, "rung toy-1")
	must(t, x.worker("toy-1", 1, "inbox"), 0, "still there?")
}

// Mail to a worker name with no open spawn is stored with a warning that a later spawn
// won't read it (bees-13.9, B: mail is scoped to the open spawn); a spawn then reads its
// brief alone.
func TestSendToANameWithNoOpenSpawnWarns(t *testing.T) {
	x := newToy(t)
	x.start()
	must(t, x.queen("send", "toy-1", "early"), 0, "warning: toy-1 is neither queen-toy nor a worker spawned", "a worker spawned as toy-1 later reads only mail sent after its spawn")
	x.spawn("toy-1", "branch")
	r := x.worker("toy-1", 1, "inbox")
	must(t, r, 0, "work on toy-1")
	if strings.Contains(r.String(), "early") {
		t.Fatalf("the spawn read mail sent before it:\n%s", r)
	}
}

// The rollout's in-flight workers (bees-13.9 review SPEC1): the old bs's end removed the
// worktree and kept the branch. A spawn of the bead adopts that branch, recorded by its
// ended spawn, in a worktree made again where it was, so the work goes on; never "branch
// already exists" with prune keeping the unmerged branch forever (motif 5).
func TestSpawnAdoptsTheBranchAnEndedSpawnRecorded(t *testing.T) {
	x := newToy(t)
	dir := filepath.Join(x.repo+"-WT", "toy-9")
	x.git(x.repo, "worktree", "add", "-q", "-b", "toy-9", dir)
	x.commit(dir, "old.txt")
	x.git(x.repo, "worktree", "remove", dir) // the old bs end: worktree gone, branch kept
	if err := os.MkdirAll(filepath.Dir(x.logbook), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", x.logbook)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE entries (id TEXT PRIMARY KEY, ts TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('message','received','rang','spawned','ended')),
  actor TEXT NOT NULL, subject TEXT, ref TEXT REFERENCES entries(id),
  data TEXT CHECK (data IS NULL OR json_valid(data))) STRICT;
INSERT INTO entries VALUES ('s1','2026-10-01T00:00:00.000Z','spawned','queen-toy','toy-9',NULL,json_object('bead','toy-9','workspace','w9','pane','w9:p1','worktree',?1,'branch','toy-9','repo',?2,'base',?2));
INSERT INTO entries VALUES ('e1','2026-10-01T00:01:00.000Z','ended','queen-toy','toy-9','s1',NULL);`, dir, x.repo)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	x.bead("toy-9", "go on")
	must(t, x.queen("spawn", "--label", "x", "--goal", "branch", "toy-9"), 0, "spawned toy-9", "branch toy-9 its last spawn left")
	if _, err := os.Stat(filepath.Join(dir, "old.txt")); err != nil {
		t.Fatalf("the adopted branch's work isn't in the worktree: %v", err)
	}
	must(t, x.worker("toy-9", 1, "done"), 0, "branch_ahead: 1 commit ahead of main")
}

// Cross-project mail goes through the same old-session refusal as any command: a target
// logbook where a session the old bs started is still open is refused and left unmigrated
// (bees-13.9 review SPEC3).
func TestSendAcrossProjectsRefusesALogbookWithOldSessions(t *testing.T) {
	x := newToy(t)
	x.start()
	other := filepath.Join(filepath.Dir(x.logbook), "other.db")
	db, err := sql.Open("sqlite", other)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE entries (id TEXT PRIMARY KEY, ts TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('message','received','rang','spawned','ended')),
  actor TEXT NOT NULL, subject TEXT, ref TEXT REFERENCES entries(id),
  data TEXT CHECK (data IS NULL OR json_valid(data))) STRICT;
INSERT INTO entries VALUES ('s1','2026-10-01T00:00:00.000Z','spawned','queen-other','other-1',NULL,'{"bead":"other-1","workspace":"w2","pane":"w2:p1","worktree":"/r/other-WT/other-1","branch":"other-1","repo":"/r/other"}');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	must(t, x.queen("send", "--project", "other", "queen-other", "hi"), 2, "other.db has an open spawn the old bs started", "other-1", "nothing stored")
	if _, err := os.Stat(other + ".pre-3"); !os.IsNotExist(err) {
		t.Fatalf("send migrated the other project's logbook: %v", err)
	}
}

// startsRec is the fake harness, recording each start it prepares.
// bees-13.13.6: a worker's own bs call records what its session says of itself (agy's
// conversation id, beesfake's BEESFAKE_CONVERSATION), once per change; the keeper's --as
// peek records nothing; the resume continues the recorded conversation even before it read
// its inbox, and a respawn starts with none.
func TestAResumeContinuesTheObservedConversation(t *testing.T) {
	x := newToy(t)
	rec := &startsRec{Harness: x.harness}
	x.ports.Harness = rec
	sp := x.spawn("toy-1", "branch")
	x.harness.Step(t, "toy-1", "env", "BEESFAKE_CONVERSATION=c1")
	x.harness.Await(t, "toy-1", 1)
	must(t, x.worker("toy-1", 2, "status"), 0)
	must(t, x.worker("toy-1", 3, "status"), 0)
	x.env = append(slices.Clone(x.env), "BEESFAKE_CONVERSATION=keepers")
	must(t, x.keeper("inbox", "--as", "toy-1"), 0, "peeked as toy-1")
	observed := func() []string {
		var out []string
		rows, err := sql.Open("sqlite", "file:"+x.logbook+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		r, err := rows.Query(`SELECT actor, ref, data FROM entries WHERE kind = 'observed' ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for r.Next() {
			var actor, ref, data string
			if err := r.Scan(&actor, &ref, &data); err != nil {
				t.Fatal(err)
			}
			out = append(out, actor+" "+ref+" "+data)
		}
		return out
	}
	if got, want := observed(), []string{"toy-1 " + string(sp.ID) + ` {"conversation":"c1"}`}; !slices.Equal(got, want) {
		t.Fatalf("observed entries = %q, want %q", got, want)
	}
	x.kill("toy-1")
	must(t, x.queen("spawn", "--goal", "branch", "toy-1"), 0, "resumed toy-1")
	if st := rec.all(); len(st) != 2 || st[0].Continue || !st[1].Continue || st[1].Record["conversation"] != "c1" || st[1].Record["state"] == "" {
		t.Fatalf("the spawn's and the resume's starts: %+v", st)
	}
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	x.spawn("toy-1", "branch")
	if sp2, _, err := x.open().LatestSpawn(context.Background(), "toy-1"); err != nil || sp2.Observed != nil {
		t.Fatalf("a respawn's observed = %v, %v", sp2.Observed, err)
	}
}

type startsRec struct {
	fake.Harness
	mu     sync.Mutex
	starts []port.Start
}

func (h *startsRec) Prepare(st port.Start) (port.Launch, error) {
	h.mu.Lock()
	h.starts = append(h.starts, st)
	h.mu.Unlock()
	return h.Harness.Prepare(st)
}

func (h *startsRec) all() []port.Start {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.starts)
}

// A spawn the old bs started is never healed, even when its recorded pane runs a harness
// with --name <worker> (the old bs's live worker, at the rollout): it stays refused, and
// bs end leaves its workspace open (bees-13.9 review SPEC7, motif 12).
func TestAnOldBsSpawnIsNeverHealed(t *testing.T) {
	x := newToy(t)
	ctx := context.Background()
	out, err := x.herdr.Run(ctx, "", "herdr", "workspace", "create", "--cwd", x.repo, "--label", "old worker", "--no-focus")
	if err != nil {
		t.Fatal(err)
	}
	var ws struct {
		Result struct {
			Workspace struct {
				ID string `json:"workspace_id"`
			} `json:"workspace"`
			Pane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &ws); err != nil {
		t.Fatal(err)
	}
	if _, err := x.herdr.Run(ctx, "", "herdr", "agent", "start", "toy-9", "--kind", "claude", "--pane", ws.Result.Pane.ID,
		"--timeout", "5000", "--", "--name", "toy-9", "--state", x.harness.Dir("toy-9")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(x.logbook), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", x.logbook)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE entries (id TEXT PRIMARY KEY, ts TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('message','received','rang','spawned','ended')),
  actor TEXT NOT NULL, subject TEXT, ref TEXT REFERENCES entries(id),
  data TEXT CHECK (data IS NULL OR json_valid(data))) STRICT;
INSERT INTO entries VALUES ('s1','2026-10-01T00:00:00.000Z','spawned','queen-toy','toy-9',NULL,json_object('bead','toy-9','workspace',?1,'pane',?2,'worktree','/r/toy-WT/toy-9','branch','toy-9','repo','/r/toy'));`,
		ws.Result.Workspace.ID, ws.Result.Pane.ID)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	must(t, x.keeper("status"), 0, "stuck: toy-9: started by the old bs")
	must(t, x.queen("inbox"), 2, "has an open spawn the old bs started", "toy-9")
	must(t, x.queen("end", "toy-9"), 0, "left open", "ended toy-9")
	if _, ok := x.herdr.Agent(herdr.Default, "toy-9"); !ok {
		t.Fatal("bs end stopped the old bs's worker")
	}
}

// The resume hint names the --goal, and runs as printed; a discarded goal gets no resume
// hint (bees-13.9 review S5).
func TestTheResumeHintRunsAsPrinted(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "branch:ready")
	x.kill("toy-1")
	r := x.keeper("status")
	must(t, r, 0, "bs spawn --goal branch:ready toy-1 resumes it")
	_, hint, _ := strings.Cut(r.String(), "→ bs ")
	hint, _, _ = strings.Cut(hint, " resumes it")
	must(t, x.queen(strings.Fields(hint)...), 0, "resumed toy-1")
	x.kill("toy-1")
	x.env = append(x.env, "BS_PROJECT=")
	must(t, x.keeper("goal", "toy-1", "discard"), 0)
	if r := x.keeper("status"); strings.Contains(r.String(), "resumes it") {
		t.Fatalf("a discarded goal's resume hint:\n%s", r)
	}
}

// An unreadable logbook is exit 6, nothing stored, with a next: (bees-13.9 re-review 2):
// the keeper's identity reads every logbook first, and fails closed (bees-13.49).
func TestSendAcrossProjectsToAnUnreadableLogbook(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a 0000 file")
	}
	x := newToy(t)
	x.start()
	other := filepath.Join(filepath.Dir(x.logbook), "other.db")
	lb, _, err := logbook.Create(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	lb.Close()
	if err := os.Chmod(other, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(other, 0o600) })
	must(t, x.queen("send", "--project", "other", "queen-other", "hi"), 6, "can't read logbook "+other+" to find who runs it", "next: check "+other)
}

// adapters makes this bs's adapters by name, as cmd/bs does, with beesfake behind each of
// kinds: a harness outside them has no adapter here.
func (x *toy) adapters(kinds ...string) {
	x.ports.Harness = nil
	x.ports.NewHarness = func(name string, _ cli.HarnessOpts) port.Harness {
		if slices.Contains(kinds, name) {
			return x.harness
		}
		return nil
	}
}

// A dead worker recorded on a harness this bs has no adapter for is refused, never resumed
// on one that can't take its conversation (ADR-0010's switch refusal; bees-13.9 re-review 7).
func TestAResumeOnAHarnessWithNoAdapterIsRefused(t *testing.T) {
	x := newToy(t)
	x.adapters("claude_code")
	x.start()
	x.bead("toy-5", "work")
	g, err := goal.Parse("branch")
	if err != nil {
		t.Fatal(err)
	}
	lb, _, err := logbook.Create(context.Background(), x.logbook)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lb.RecordSpawn(context.Background(), "queen-toy", logbook.Spawn{Name: "toy-5", Bead: "toy-5", Harness: "omp", Goal: g,
		Herdr:     logbook.Pane{Session: x.session, Workspace: "w9", Pane: "w9:p1"},
		Workspace: logbook.Workspace{Kind: "git_worktree", Dir: filepath.Join(x.repo+"-WT", "toy-5"), Repo: x.repo, Branch: "toy-5", Base: "main"}}, "brief")
	lb.Close()
	if err != nil {
		t.Fatal(err)
	}
	must(t, x.queen("spawn", "--goal", "branch", "toy-5"), 2, "toy-5 was spawned on harness omp, which this bs has no adapter for; nothing started",
		"next: bs end --incomplete toy-5, then bs spawn --goal branch toy-5")
}

// bs spawn --harness starts that one spawn on another harness than the configured one and
// records it; its resume restarts the recorded harness, whatever the config says, and
// refuses another --harness (bees-13.13.12; motif 14). A harness with no adapter makes
// nothing.
func TestSpawnHarnessIsRecordedAndResumed(t *testing.T) {
	x := newToy(t)
	x.adapters("claude_code", "agy")
	x.bead("toy-1", "work")
	x.bead("toy-2", "work")
	must(t, x.queen("spawn", "--goal", "branch", "--label", "toy 2", "--harness", "omp", "toy-2"), 2, "toy-2: this bs has no adapter for harness omp; nothing was made")
	if _, err := os.Stat(filepath.Join(x.repo+"-WT", "toy-2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused --harness made a worktree: %v", err)
	}
	must(t, x.queen("spawn", "--goal", "branch", "--label", "toy 1", "--harness", "agy", "toy-1"), 0, "spawned toy-1")
	sp, ok, err := x.open().LatestSpawn(context.Background(), "toy-1")
	if err != nil || !ok || sp.Harness != "agy" {
		t.Fatalf("the spawn's recorded harness: %q (%v, %v)", sp.Harness, ok, err)
	}
	must(t, x.worker("toy-1", 1, "inbox"), 0)
	x.kill("toy-1")
	must(t, x.queen("spawn", "--goal", "branch", "--harness", "claude_code", "toy-1"), 2,
		"toy-1 is spawned and not ended, on harness agy, not claude_code; a resume keeps its harness, so leave --harness off")
	must(t, x.queen("spawn", "--goal", "branch", "toy-1"), 0, "resumed toy-1")
	st, ok, err := x.open().LatestStart(context.Background(), sp.ID)
	if err != nil || !ok || st.Harness != "agy" || st.Replaces == "" {
		t.Fatalf("the resume's start: %+v (%v, %v)", st, ok, err)
	}
}

// bees-13.13.13: a herdr server started over ssh hands SSH_* to every pane, and agy reads
// them as a remote session ("not signed in"): what a harness must not inherit is unset in
// the worker's pane before herdr types its launch there, and the rest stays.
func TestAWorkerDoesNotInheritWhatItsHarnessUnsets(t *testing.T) {
	x := newToy(t)
	x.herdr.Env = append(x.herdr.Env, "SSH_TTY=/dev/pts/9", "SSH_AUTH_SOCK=/tmp/agent")
	h := &unsetting{Harness: x.harness}
	x.ports.Harness = h
	x.spawn("toy-1", "branch")
	must(t, x.worker("toy-1", 1, "status"), 0)
	env := h.seen()
	if slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "SSH_TTY=") }) || !slices.Contains(env, "SSH_AUTH_SOCK=/tmp/agent") {
		t.Fatalf("the worker's env: %q", env)
	}
}

// bees-14.7: a harness whose program isn't its kind's own (an agy-compatible build named
// otherwise) can't be started by herdr's --kind, which types only the kind's program:
// bs types its launch into the pane, after what it must not inherit is unset, records the
// program on the spawn, and knows the process as the worker's by that record, though its
// name is past the 15 bytes the kernel keeps.
func TestAWorkerRunsAProgramNotNamedAfterItsKind(t *testing.T) {
	x := newToy(t)
	jet := filepath.Join(t.TempDir(), "jetski-agy-compatible")
	if err := os.Symlink(fake.Beesfake(t), jet); err != nil {
		t.Fatal(err)
	}
	x.herdr.Env = append(x.herdr.Env, "SSH_TTY=/dev/pts/9")
	h := &unsetting{Harness: fake.Harness{State: x.harness.State, Bin: jet}}
	x.ports.Harness = h
	sp := x.spawn("toy-1", "branch")
	if sp.Program != jet {
		t.Fatalf("the spawn's recorded program: %q", sp.Program)
	}
	st, ok, err := x.open().LatestStart(context.Background(), sp.ID)
	if err != nil || !ok || st.Late {
		t.Fatalf("its start: %+v (%v, %v)", st, ok, err)
	}
	if name, _ := proc.Name(st.PID); runtime.GOOS == "linux" && name != "jetski-agy-comp" {
		t.Errorf("pid %d is %q, not the program", st.PID, name)
	}
	must(t, x.worker("toy-1", 1, "status"), 0)
	if env := h.seen(); slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "SSH_TTY=") }) {
		t.Fatalf("the worker's env: %q", env)
	}
	must(t, x.keeper("status"), 0, "toy-1")
}

// unsetting is the fake harness unsetting SSH_TTY, as agy does, keeping the env of its
// worker's last bs call.
type unsetting struct {
	fake.Harness
	mu  sync.Mutex
	env []string
}

func (h *unsetting) Prepare(st port.Start) (port.Launch, error) {
	l, err := h.Harness.Prepare(st)
	l.Unset = []string{"SSH_TTY"}
	return l, err
}

func (h *unsetting) Observe(env []string) map[string]string {
	h.mu.Lock()
	h.env = slices.Clone(env)
	h.mu.Unlock()
	return h.Harness.Observe(env)
}

func (h *unsetting) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.env
}
