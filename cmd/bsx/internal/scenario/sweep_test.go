package scenario_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/fake"
)

// The pilot's round-11 sweep (docs/pilot-bees-12.md on origin/pilot/bees-12), one test
// per row. The baseline and the gate rows are TestSpawnHandoffEndPruneOnABranchGoal and
// TestTheGateRefusesBackgroundWork.

// Round 11: worker ↔ worker mail; a running worker is rung, through its 0600 socket.
func TestWorkersMailEachOtherAndAreRung(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "branch")
	x.spawn("toy-2", "branch")
	must(t, x.worker("toy-1", 1, "send", "toy-2", "rebase on mine"), 0, "sent to toy-2")
	r := x.worker("toy-2", 1, "inbox")
	must(t, r, 0, "from toy-1", "rebase on mine")
	if rings := x.harness.Rings("toy-2"); len(rings) == 0 || !strings.Contains(rings[len(rings)-1], "1 unread (toy-1)") {
		t.Fatalf("toy-2's rings: %q", rings)
	}
}

// Round 11: herdr labels read <project> · <label>.
func TestHerdrLabelsNameTheProject(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "branch")
	out, err := x.herdr.Run(context.Background(), "", "herdr", "--session", "toy", "workspace", "list")
	if err != nil || !strings.Contains(string(out), `"label":"toy · work on toy-1"`) {
		t.Fatalf("workspace list: %s, %v", out, err)
	}
}

// Round 11, bees-12.37: a worker killed without bs end shows not running, never absent.
func TestAKilledWorkerShowsNotRunning(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "branch")
	pid, ok := x.herdr.Agent("toy", "toy-1")
	if !ok {
		t.Fatal("no agent")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if _, ok := x.herdr.Agent("toy", "toy-1"); !ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r := x.keeper("status")
	must(t, r, 0, "toy-1")
	if !strings.Contains(lineOf(r.out, "toy-1"), "  no  ") {
		t.Fatalf("status:\n%s", r)
	}
}

// Round 11: bs prune says why it keeps a workspace.
func TestPruneSaysWhyItKeepsAWorkspace(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	x.write(filepath.Join(sp.Workspace.Dir, "scratch.txt"), "x")
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir, "not merged into main")
	x.git(x.repo, "merge", "-q", "--ff-only", "toy-1")
	must(t, x.queen("prune"), 3, "uncommitted or untracked")
	if ld, ok, _ := x.open().LandedOf(context.Background(), sp.ID); !ok || ld.By != "prune" {
		t.Fatalf("prune didn't complete the goal: %+v %v", ld, ok)
	}
	os.Remove(filepath.Join(sp.Workspace.Dir, "scratch.txt"))
	must(t, x.queen("prune"), 0, "removed")
}

func TestRound11NoneOnANonGitDirectory(t *testing.T) {
	later(t, "bees-13.10", "workspace none")
}
func TestRound11ADeliveryFragmentForACLFlow(t *testing.T) {
	later(t, "bees-13.11", "command workspaces and custom goals")
}
func TestRound11StaticGuards(t *testing.T) { later(t, "bees-13.6", "the guards matrix") }
func TestRound11WorkersRunTheBuildUnderTest(t *testing.T) {
	later(t, "bees-13.3", "worker.path and the live rig")
}
func TestRound11AMovedPaneStaysRunningAndIsRung(t *testing.T) {
	later(t, "bees-13.4", "identity and rings of a moved pane")
}
func TestRound11EndRefusesAWorkspaceHoldingAnotherWorkersPane(t *testing.T) {
	later(t, "bees-13.4", "teardown guests (bees-12.36)")
}
func TestRound11CrossProjectSendAndReply(t *testing.T) {
	later(t, "bees-13.8", "cross-project mail (bees-12.38)")
}
func TestRound11EndForceOnAMovedWorker(t *testing.T) {
	later(t, "bees-13.4", "stop() of a moved pane (bees-12.46)")
}
func TestRound11AFreshAgentSetsUpFromSetupAgentMD(t *testing.T) {
	later(t, "bees-13.8", "SETUP-AGENT.md")
}
func TestRound11Doctor(t *testing.T) { later(t, "bees-13.8", "bs doctor") }
func TestRound11TeardownLeavesNoLeaks(t *testing.T) {
	later(t, "bees-13.3", "the live rig's leak check")
}

// Pilot bugs.

// bees-12.33: a new pane's shell not yet at its prompt (agent_pane_busy) is retried.
func TestABusyPaneIsRetried(t *testing.T) {
	x := newToy(t)
	x.herdr.Busy = 2
	x.spawn("toy-1", "branch")
	if x.herdr.Busy != 0 {
		t.Fatalf("%d busy refusals left: the spawn didn't retry through them", x.herdr.Busy)
	}
}

// bees-12.49: herdr gave up waiting (timeout) while the worker started: the start is
// found and recorded, and spawn succeeds.
func TestAStartHerdrTimedOutIsAdopted(t *testing.T) {
	x := newToy(t)
	x.herdr.StartFails = "timeout"
	sp := x.spawn("toy-1", "branch")
	if _, ok, _ := x.open().LatestStart(context.Background(), sp.ID); !ok {
		t.Fatal("no start recorded")
	}
}

// bees-12.47: inside a recorded pane the pane decides; a stray BS_* is ignored, with a
// warning.
func TestBSVariablesAreIgnoredInsideARecordedPane(t *testing.T) {
	x := newToy(t)
	x.herdr.Env = append(x.herdr.Env, "BS_PROJECT=elsewhere")
	x.spawn("toy-1", "verdict")
	must(t, x.worker("toy-1", 1, "inbox"), 0, "ignoring BS_PROJECT", "You own toy-1")
}

// bees-12.17 (tokens cut at 80) is TestFakeHerdrCutsTokensAt80: the fake cuts, the client
// refuses a token herdr would cut.

// The live-test and docs bugs belong to the rig and setup beads.
func TestPilotBugsOfTheLiveRig(t *testing.T) {
	later(t, "bees-13.3", "the rig: 12.19, 12.22, 12.24, 12.26, 12.30, 12.34, 12.35, 12.40")
}
func TestPilotBugsOfSetupDocs(t *testing.T) { later(t, "bees-13.8", "SETUP-AGENT.md: 12.42, 12.48") }
func TestPilotBug1251PromptFriction(t *testing.T) {
	later(t, "bees-13.9", "the role prompts rewritten (12.51); positional bs done is TestDoneRefusesAPositionalReport")
}
func TestPilotBug1220MergeGrantOnAnUnprotectedRepo(t *testing.T) {
	later(t, "bees-13.6", "allow_merge_into")
}
func TestPilotBug1221GuardGlobs(t *testing.T) { later(t, "bees-13.6", "guard globs") }
func TestPilotBug1227AgyRefusals(t *testing.T) {
	later(t, "bees-13.13", "the agy adapter's refusals")
}
func TestPilotBug1228NoneInAGitCheckout(t *testing.T) { later(t, "bees-13.10", "workspace none") }
func TestPilotBug1229PromptsUnderNone(t *testing.T) {
	later(t, "bees-13.10", "workspace none prompt fragments")
}
func TestPilotBug1231EndUnderNoneStopsTheWorker(t *testing.T) {
	later(t, "bees-13.10", "workspace none teardown")
}
func TestPilotBug1232ANonGitQueenNeedsNoBSProject(t *testing.T) {
	later(t, "bees-13.10", "workspace none")
}
func TestPilotBug1241PruneAfterEndForce(t *testing.T) {
	later(t, "bees-13.5", "prune of a forced end, and the branch's removal")
}
func TestPilotBug1243APaneActsOnItsOwnProject(t *testing.T) {
	later(t, "bees-13.4", "the pane scan across logbooks")
}
func TestPilotBug1244DoctorAndConfigGaps(t *testing.T) {
	later(t, "bees-13.8", "bs doctor warns of a PR goal with no remote and a stale herdr server; bs config lists guards")
}
func TestPilotBug1245DoctorNamesAStrayBSProject(t *testing.T) { later(t, "bees-13.8", "bs doctor") }

// bees-12.50, 12.52: bs done's own pipeline (bs done | tail) is the call, not background
// work: the gate skips the caller's process group. A reply-wait loop running bs is one of
// its ancestors (beesfake itself here), skipped too.
func TestTheGateSkipsDonesOwnPipeline(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	x.harness.Step(t, "toy-1", "bs|", "done")
	r := x.harness.Await(t, "toy-1", 1)
	must(t, result{r.Code, r.Stdout, r.Stderr}, 0, "handed off")
}

func TestPilotBug1250AReplyWaitLoopInTheBackground(t *testing.T) {
	later(t, "bees-13.4", "a reply-wait loop left running with & counts; say so in the prompts")
}

// bees-12.23 is TestConcurrentFirstOpensOfANewLogbookAllSucceed (logbook); bees-12.25 (a
// semantic break between green PRs) is go test ./... over the whole tree before each PR.

// ADR-0010's goal rules.

// --goal is required, and every bad value is named at once (Rule D, Rule G).
func TestSpawnRequiresAGoalAndNamesEveryBadOne(t *testing.T) {
	x := newToy(t)
	x.bead("toy-1", "x")
	must(t, x.queen("spawn", "toy-1"), 2, "--goal is required")
	must(t, x.queen("spawn", "--goal", "prr", "--goal", "push", "toy-1"), 2, `"prr"`, `"push"`)
	if _, ok, _ := x.open().LatestSpawn(context.Background(), "toy-1"); ok {
		t.Fatal("a refused spawn was recorded")
	}
}

// bs end --incomplete is refused when nothing was handed off.
func TestEndIncompleteNeedsAHandoff(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "branch")
	must(t, x.queen("end", "--incomplete", "toy-1"), 2, "nothing was handed off")
}

// A check that can't run stops bs done at exit 6, never a fail.
func TestDoneStopsAtACheckThatCantRun(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	if err := os.RemoveAll(filepath.Join(sp.Workspace.Dir, ".git")); err != nil {
		t.Fatal(err)
	}
	must(t, x.worker("toy-1", 1, "done"), 6, "couldn't run")
}

// A spawn written before goals reads as --goal legacy.
func TestALegacySpawnReadsAsLegacy(t *testing.T) {
	x := newToy(t)
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
INSERT INTO entries VALUES ('s1','2026-10-01T00:00:00.000Z','spawned','queen-toy','toy-9',NULL,'{"bead":"toy-9","workspace":"w2","pane":"w2:p1","worktree":"/r/toy-WT/toy-9","branch":"toy-9","repo":"/r/toy"}');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	r := x.keeper("status")
	must(t, r, 0, "--goal legacy", "no start recorded (legacy)")
	if _, err := os.Stat(x.logbook + ".pre-2"); err != nil {
		t.Fatalf("no copy aside: %v", err)
	}
	// Its pane and pid are on herdr's default session, bs's until the swap: bsx never
	// closes a workspace there or signals a process it recorded.
	must(t, x.queen("end", "--incomplete", "toy-9"), 2, "herdr's default session", "bees-13.9")
}

// A legacy worktree prunes on proof 1 against the repo's default branch: its recorded
// base was a checkout path, never a ref (bees-11.3), so merge-base can't be given it.
func TestPruneTakesALegacyWorktreeOnTheDefaultBranch(t *testing.T) {
	x := newToy(t)
	merged, open := filepath.Join(x.repo+"-WT", "toy-8"), filepath.Join(x.repo+"-WT", "toy-9")
	x.git(x.repo, "worktree", "add", "-q", "-b", "toy-8", merged, "main")
	x.git(x.repo, "worktree", "add", "-q", "-b", "toy-9", open, "main")
	x.commit(open, "work.txt")
	if err := os.MkdirAll(filepath.Dir(x.logbook), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", x.logbook)
	if err != nil {
		t.Fatal(err)
	}
	row := func(id, name, dir string) string {
		return fmt.Sprintf(`INSERT INTO entries VALUES ('%[1]s','2026-10-01T00:00:00.000Z','spawned','queen-toy','%[2]s',NULL,'{"bead":"%[2]s","workspace":"w2","pane":"w2:p1","worktree":"%[3]s","branch":"%[2]s","repo":"%[4]s","base":"%[4]s"}');
INSERT INTO entries VALUES ('e%[1]s','2026-10-01T00:00:01.000Z','ended','queen-toy','%[2]s','%[1]s',NULL);`, id, name, dir, x.repo)
	}
	_, err = db.Exec(`CREATE TABLE entries (id TEXT PRIMARY KEY, ts TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('message','received','rang','spawned','ended')),
  actor TEXT NOT NULL, subject TEXT, ref TEXT REFERENCES entries(id),
  data TEXT CHECK (data IS NULL OR json_valid(data))) STRICT;` + row("s1", "toy-8", merged) + row("s2", "toy-9", open))
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	must(t, x.queen("prune"), 3, "removed "+merged, "toy-9 is not merged into main")
	if _, err := os.Stat(open); err != nil {
		t.Fatalf("prune removed the unmerged %s: %v", open, err)
	}
}

func TestPRGoalHandoffChecks(t *testing.T) {
	later(t, "bees-13.6", "pr, pr:ready, pr:draft handoff and completion")
}
func TestPRsAccumulateAndReplace(t *testing.T) {
	later(t, "bees-13.6", "--pr accumulation and --replaces")
}
func TestAPRMustHoldAWorkspaceCommit(t *testing.T) {
	later(t, "bees-13.6", "workspace commits from the reflog")
}
func TestPushAndReviewGoals(t *testing.T) { later(t, "bees-13.6", "push= and review=") }
func TestBsGoalAmendWaiveDiscard(t *testing.T) {
	later(t, "bees-13.6", "bs goal, and discard by the keeper only")
}
func TestBsCheckWritesNothing(t *testing.T) { later(t, "bees-13.6", "bs check") }
func TestNoteAndBeadsGoals(t *testing.T)    { later(t, "bees-13.7", "note and beads goals") }
func TestCustomGoalsAreFrozenAtSpawn(t *testing.T) {
	later(t, "bees-13.11", "[goal.<name>] and cmd: checks frozen in spawned")
}

// Rule G.

// bsx refuses the default config dir, and a session without its config dir.
func TestBsxRefusesTheDefaultConfigDirAndAMissingSessionDir(t *testing.T) {
	x := newToy(t)
	x.env = append(x.env, "HERDR_SESSION=")
	must(t, x.keeper("status"), 2, "refuses the default config dir")
	x.env = append(x.env, "HERDR_SESSION=nosuch")
	must(t, x.keeper("status"), 2, "no config dir")
}

// A start bs can't confirm is exit 8, says what to run next, and bs end clears it.
func TestAnUnconfirmedStartIsExit8AndEndClearsIt(t *testing.T) {
	x := newToy(t)
	x.herdr.Programs["claude"] = "/bin/true"
	x.ports.Timing.Confirm = 300 * time.Millisecond
	x.bead("toy-1", "x")
	must(t, x.queen("spawn", "--goal", "verdict", "toy-1"), 8, "start unconfirmed", "next: bsx end toy-1 --incomplete")
	must(t, x.queen("end", "toy-1"), 9)                                // a verdict never handed off is incomplete
	must(t, x.queen("end", "--incomplete", "toy-1"), 0, "ended toy-1") // the abort of a spawn that never ran (motif 5)
	sp, _, _ := x.open().LatestSpawn(context.Background(), "toy-1")
	if e, ok, _ := x.open().EndOf(context.Background(), sp.ID); !ok || !e.Incomplete {
		t.Fatalf("ended = %+v, %v", e, ok)
	}
	x.herdr.Programs["claude"] = fake.Beesfake(t)
	x.ports.Timing.Confirm = 10 * time.Second
	must(t, x.queen("spawn", "--goal", "verdict", "toy-1"), 2, "already exists") // its worktree waits for prune
}

// A spawn whose record fails takes back the worktree it made (motif 8): a read-only
// logbook makes the record fail after herdr made the worktree.
func TestASpawnThatCantBeRecordedLeavesNoWorktree(t *testing.T) {
	x := newToy(t)
	must(t, x.keeper("status"), 0) // creates the logbook
	if err := os.Chmod(x.logbook, 0o400); err != nil {
		t.Fatal(err)
	}
	x.bead("toy-1", "x")
	must(t, x.queen("spawn", "--goal", "verdict", "toy-1"), 1, "couldn't record the spawn")
	if _, err := os.Stat(filepath.Join(filepath.Dir(x.repo), "toy-WT", "toy-1")); !os.IsNotExist(err) {
		t.Fatalf("worktree left behind: %v", err)
	}
	if x.hasBranch("toy-1") {
		t.Fatal("branch toy-1 left behind")
	}
}

// A branch already named for the worker refuses the spawn before anything is made
// (exit 2), naming how to clear it: herdr would fail on it after the checks.
func TestSpawnRefusesABranchThatAlreadyExists(t *testing.T) {
	x := newToy(t)
	x.git(x.repo, "branch", "toy-1")
	x.bead("toy-1", "x")
	must(t, x.queen("spawn", "--goal", "branch", "toy-1"), 2, "already has a branch toy-1", "next: bsx prune", "branch -d toy-1")
	if _, ok, err := x.open().LatestSpawn(context.Background(), "toy-1"); err != nil || ok {
		t.Fatalf("a refused spawn was recorded: %v, %v", ok, err)
	}
}

func TestSpawnRefusesAPRGoalWithoutARemote(t *testing.T) { later(t, "bees-13.6", "remote refusals") }
func TestSpawnRefusesACustomArgv0NotOnPath(t *testing.T) { later(t, "bees-13.11", "cmd: checks") }
func TestSpawnRefusesACommandKindWithoutClean(t *testing.T) {
	later(t, "bees-13.11", "[workspace.command]")
}

// helpers

func lineOf(s, word string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, word) {
			return l
		}
	}
	return ""
}

// A bead the tracker says doesn't exist refuses the spawn (exit 2) and names it.
func TestSpawnOfNoSuchBeadIsRefused(t *testing.T) {
	x := newToy(t)
	must(t, x.queen("spawn", "--goal", "verdict", "toy-404"), 2, "no bead toy-404", "next: bd show toy-404")
}

// With no harness adapter (bsx's main until bees-13.3), mail to a running worker is
// stored and says it wasn't rung, never a panic.
func TestSendWithNoHarnessStoresAndSaysNotRung(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	x.ports.Harness = nil
	must(t, x.queen("send", "toy-1", "hi"), 0, "not rung", "has no ring")
}

// A bad flag is a usage refusal (exit 2), never a failure.
func TestABadFlagIsRefused(t *testing.T) {
	x := newToy(t)
	must(t, x.queen("end", "--bogus", "toy-1"), 2, "flag provided but not defined: -bogus")
}

// Mail to a bead id reaches its worker: the id names the session (ADR-0006, motif 3).
func TestSendToABeadIDReachesItsWorker(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1.2", "verdict")
	must(t, x.queen("send", "toy-1.2", "hi"), 0, "sent to toy-1-2")
	must(t, x.worker("toy-1-2", 1, "inbox"), 0, "from queen-toy", "hi")
}
