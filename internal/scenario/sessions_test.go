package scenario_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/logbook"
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
	must(t, x.queenRuns(1, "spawn", "--goal", "branch", "toy-1"), 0, "spawned toy-1")
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

// bees-13.14: a keeper's herdr shell is a pane no entry recorded: --as names the session
// there as outside herdr, and without it bs says so.
func TestAnUnrecordedPaneTakesAs(t *testing.T) {
	x := newToy(t)
	x.start()
	x.shell(x.session, "keeper", x.repo)
	must(t, x.worker("keeper", 1, "send", "--as", "keeper", "queen-toy", "hi"), 0, "to queen-toy")
	must(t, x.worker("keeper", 2, "inbox"), 2, "not in a recorded herdr pane: name the session with --as")
	must(t, x.keeper("inbox", "--as", "queen-toy"), 0, "from keeper", "hi")
}

// bees-13.15, bees-12.43: a worker finds its project from its pane, wherever its cwd is:
// outside every repo, or inside another project's.
func TestAPaneActsOnItsOwnProjectWhereverItsCwdIs(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	other := filepath.Join(filepath.Dir(x.repo), "other")
	x.git(filepath.Dir(x.repo), "init", "-q", other)
	for i, dir := range []string{os.TempDir(), other} {
		x.harness.Step(t, "toy-1", "cd", dir)
		must(t, x.worker("toy-1", 2*i+2, "status"), 0, "toy-1")
	}
	must(t, x.worker("toy-1", 5, "inbox"), 0, "You own toy-1")
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
	must(t, x.worker("toy-1", 1, "inbox"), 0, "You own toy-1")
	x.write(filepath.Join(dir, "broken.db"), "not a logbook")
	must(t, x.worker("toy-1", 2, "inbox"), 6, "broken.db")
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
	must(t, x.worker("elsewhere", 1, "inbox"), 2, "not in a recorded herdr pane")
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
	must(t, x.queen("spawn", "--goal", "verdict", "toy-1"), 8, "start unconfirmed", "can't read the table")
	x.herdr.FailStarts(nil)
	sp, _, _ := x.open().LatestSpawn(context.Background(), "toy-1")
	x.herdr.InfoFails = 1000
	r := x.keeper("status")
	if !strings.HasPrefix(cell(r.out, "toy-1", "RUNNING"), "unknown: can't look for toy-1's harness") {
		t.Fatalf("status with herdr down:\n%s", r)
	}
	if _, ok, _ := x.open().LatestStart(context.Background(), sp.ID); ok {
		t.Fatal("a failed heal probe wrote a start")
	}
	x.herdr.InfoFails = 0
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
	must(t, x.queen("spawn", "--goal", "verdict", "toy-1"), 8)
	x.herdr.FailStarts(nil)
	must(t, x.worker("toy-1", 1, "inbox"), 0, "You own toy-1", "recorded late")
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
// other project's logbook, and that project's inbox shows how to reply across.
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
		"from toy-1", "your API changed?", `(reply with: bs send --project toy toy-1 "...")`)
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
	x.harness.Await(t, "toy-1", 1)
	time.Sleep(200 * time.Millisecond) // it binds its port
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
	must(t, x.queen("end", "toy-1"), 2, "moved out of its workspace", "nothing closed", "bs end toy-1 --force")
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
	must(t, x.queen("spawn", "--goal", "verdict", "toy-1"), 0, "spawned toy-1")
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
	sp := x.spawn("toy-1", "branch")
	must(t, x.worker("toy-1", 1, "inbox"), 0)
	first, _, _ := x.open().LatestStart(context.Background(), sp.ID)
	must(t, x.queen("spawn", "--goal", "branch", "toy-1"), 2, "toy-1 is already running", "next: bs send toy-1")
	x.kill("toy-1")
	must(t, x.keeper("status"), 0, "stuck: toy-1: its process is gone but it isn't ended → bs spawn --goal branch toy-1 resumes it in its pane")
	must(t, x.queen("send", "toy-1", "while you were down"), 0, "stored, not rung")
	must(t, x.queen("spawn", "--goal", "branch", "toy-1"), 0, "resumed toy-1 in pane "+sp.Herdr.Pane)
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
	if len(rings) == 0 || !strings.Contains(rings[len(rings)-1], "2 unread (queen-toy ×2)") {
		t.Fatalf("the resumed worker's rings: %q", rings)
	}
	must(t, x.queen("spawn", "--goal", "verdict", "toy-1"), 2, "it was spawned with --goal branch", "bs goal toy-1")

	sp2 := x.spawn("toy-2", "branch")
	x.kill("toy-2")
	if _, err := x.herdr.Run(context.Background(), "", "herdr", "--session", x.session, "workspace", "close", sp2.Herdr.Workspace); err != nil {
		t.Fatal(err)
	}
	must(t, x.queen("spawn", "--goal", "branch", "toy-2"), 2, "pane "+sp2.Herdr.Pane+" is gone", "bs end toy-2 --incomplete")
}
