package scenario_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmmshn/bees/cmd/bsx/internal/fake"
	"github.com/jmmshn/bees/cmd/bsx/internal/goal"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/prompt"
)

// bees-13.5's live script on fakes: a refused handoff names the uncommitted file; the
// queen squash-merges; bs end proves it by content and lands it; prune removes the
// worktree and the branch; status shows each step.
func TestASquashMergedBranchLandsAndPrunes(t *testing.T) {
	x := newToy(t)
	ctx := context.Background()
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	x.write(filepath.Join(sp.Workspace.Dir, "notes.txt"), "draft")
	must(t, x.worker("toy-1", 1, "done"), 7, "1 handoff checks failed", goal.Clean, "notes.txt")
	if r := x.keeper("status"); cell(r.out, "toy-1", "HANDOFF") != "-" || cell(r.out, "toy-1", "COMPLETE") != "no" || cell(r.out, "toy-1", "STATE") != "live" {
		t.Fatalf("status before the handoff:\n%s", r)
	}
	os.Remove(filepath.Join(sp.Workspace.Dir, "notes.txt"))
	must(t, x.worker("toy-1", 2, "done"), 0, "handed off to queen-toy")
	if r := x.keeper("status"); !strings.HasPrefix(cell(r.out, "toy-1", "HANDOFF"), "done ") || cell(r.out, "toy-1", "COMPLETE") != "no" {
		t.Fatalf("status after the handoff:\n%s", r)
	}
	must(t, x.queen("end", "toy-1"), 9, "toy-1 has work not on main")
	x.git(x.repo, "merge", "-q", "--squash", "toy-1")
	x.git(x.repo, "commit", "-qm", "squash toy-1")
	must(t, x.queen("end", "toy-1"), 0, "ended toy-1")
	if ld, ok, _ := x.open().LandedOf(ctx, sp.ID); !ok || ld.By != "end" || ld.Checks[0] != goal.BranchMerged {
		t.Fatalf("landed = %+v, %v", ld, ok)
	}
	if r := x.keeper("status"); cell(r.out, "toy-1", "STATE") != "ended" || cell(r.out, "toy-1", "COMPLETE") != "yes (end)" || strings.Contains(r.out, "stuck:") {
		t.Fatalf("status after the end:\n%s", r)
	}
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir+" and branch toy-1 (toy-1)")
	if _, err := os.Stat(sp.Workspace.Dir); !os.IsNotExist(err) {
		t.Fatalf("prune kept %s", sp.Workspace.Dir)
	}
	if out := x.git(x.repo, "branch", "--list", "toy-1"); out != "" {
		t.Fatalf("prune kept the branch: %q", out)
	}
	must(t, x.queen("prune"), 0) // nothing left: says nothing, exits 0
}

// bees-12.41: a worker ended with --force (its gate waived) still has its worktree and
// branch pruned once merged; so does one whose worktree was deleted by hand, which git
// still counts as having the branch checked out.
func TestPilotBug1241PruneAfterEndForce(t *testing.T) {
	x := newToy(t)
	sp1 := x.spawn("toy-1", "branch")
	sp2 := x.spawn("toy-2", "branch")
	for _, sp := range []string{sp1.Workspace.Dir, sp2.Workspace.Dir} {
		x.commit(sp, "work-"+filepath.Base(sp)+".txt")
	}
	must(t, x.worker("toy-1", 1, "done"), 0)
	must(t, x.worker("toy-2", 1, "done"), 0)
	x.git(x.repo, "merge", "-q", "toy-1", "toy-2")
	x.harness.Step(t, "toy-1", "child", "server")
	x.harness.Await(t, "toy-1", 2)
	must(t, x.queen("end", "toy-1"), 5, "background work")
	must(t, x.queen("end", "--force", "toy-1"), 0, "ended toy-1")
	must(t, x.queen("end", "toy-2"), 0)
	if err := os.RemoveAll(sp2.Workspace.Dir); err != nil {
		t.Fatal(err)
	}
	// Someone else's stale worktree entry: prune drops only its own (motif 12).
	stale := filepath.Join(filepath.Dir(x.repo), "stale")
	x.git(x.repo, "worktree", "add", "-q", "-b", "other", stale)
	if err := os.RemoveAll(stale); err != nil {
		t.Fatal(err)
	}
	r := x.queen("prune")
	must(t, r, 0, "removed "+sp1.Workspace.Dir+" and branch toy-1", "removed branch toy-2 (toy-2)")
	if out := x.git(x.repo, "branch", "--list", "toy-*"); out != "" {
		t.Fatalf("branches left: %q", out)
	}
	if !strings.Contains(x.git(x.repo, "worktree", "list"), "stale") {
		t.Fatal("prune dropped another worktree's stale entry")
	}
}

// Prune deletes no branch another worktree has in use (the pilot's checkedOut guard): it
// removes the workspace, keeps the branch and says where it is in use (exit 3).
func TestPruneKeepsABranchInUseElsewhere(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.git(x.repo, "merge", "-q", "toy-1")
	must(t, x.queen("end", "toy-1"), 0)
	x.git(sp.Workspace.Dir, "checkout", "-q", "--detach")
	other := filepath.Join(filepath.Dir(x.repo), "other")
	x.git(x.repo, "worktree", "add", "-q", other, "toy-1")
	at, _ := filepath.EvalSymlinks(other) // git names it resolved: /private/var on macOS
	must(t, x.queen("prune"), 3, "removed "+sp.Workspace.Dir+" (toy-1)", "kept branch toy-1 (toy-1): branch toy-1 is in use by the worktree "+at)
	x.git(x.repo, "worktree", "remove", other)
	must(t, x.queen("prune"), 0, "removed branch toy-1 (toy-1)")
}

// bees-1.34: prune never touches a remote branch, and says when one of the same name is
// left.
func TestPruneLeavesTheRemoteBranchAndSaysSo(t *testing.T) {
	x := newToy(t)
	x.git(x.repo, "remote", "add", "origin", filepath.Join(filepath.Dir(x.repo), "origin.git"))
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	x.git(x.repo, "update-ref", "refs/remotes/origin/toy-1", "toy-1")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.git(x.repo, "merge", "-q", "toy-1")
	must(t, x.queen("end", "toy-1"), 0)
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir+" and branch toy-1", "left origin/toy-1: bsx never touches a remote branch")
	if x.git(x.repo, "rev-parse", "--verify", "refs/remotes/origin/toy-1") == "" {
		t.Fatal("prune touched origin/toy-1")
	}
}

// The third proof: a merged GitHub pull request whose head is the branch tip, asked of gh
// only when the local proofs fail and origin is on GitHub. gh offline then is "can't
// tell" (exit 6), never "not merged".
func TestAMergedPullRequestProvesTheBranch(t *testing.T) {
	x := newToy(t)
	x.git(x.repo, "remote", "add", "origin", "https://github.com/x/toy.git")
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.gh.Offline = true
	must(t, x.queen("end", "toy-1"), 6, "GitHub couldn't be asked", "error connecting")
	x.gh.Offline = false
	must(t, x.queen("end", "toy-1"), 9, "toy-1 has work not on main")
	x.gh.PRs["https://github.com/x/toy/pull/1"] = &fake.PR{URL: "https://github.com/x/toy/pull/1", Head: "toy-1",
		HeadOID: x.git(x.repo, "rev-parse", "toy-1"), Base: "main", State: "MERGED"}
	must(t, x.queen("end", "toy-1"), 0, "ended toy-1")
}

// Rule G at spawn: a branch left by an earlier spawn is refused before anything is made.
func TestSpawnRefusesABranchThatExists(t *testing.T) {
	x := newToy(t)
	x.bead("toy-1", "x")
	x.git(x.repo, "branch", "toy-1")
	must(t, x.queen("spawn", "--goal", "branch", "toy-1"), 2, "branch toy-1 already exists", "next: bsx prune")
	if _, ok, _ := x.open().LatestSpawn(context.Background(), "toy-1"); ok {
		t.Fatal("a refused spawn was recorded")
	}
}

// The spawned workspace carries the worker= tag and the project in its label.
func TestTheWorkspaceIsTaggedWithItsWorker(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "branch")
	out, err := x.herdr.Run(context.Background(), "", "herdr", "--session", "toy", "workspace", "list")
	if err != nil || !strings.Contains(string(out), `"worker":"toy-1"`) {
		t.Fatalf("workspace list: %s, %v", out, err)
	}
}

// The spawn freezes its goal fragment's hash, and the worker's prompt carries the git_worktree and branch fragments' worker parts, and no
// queen part.
func TestTheWorkerPromptHasItsWorkspaceAndGoal(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	if h := sp.Goal.Parts[0].Fragment; h == "" || h != prompt.Hash("branch") {
		t.Fatalf("spawned froze fragment hash %q, want %q", h, prompt.Hash("branch"))
	}
	fs, _ := filepath.Glob(filepath.Join(x.ports.CacheDir, "toy-1-prompt-*.md"))
	if len(fs) != 1 {
		t.Fatalf("prompt files: %v", fs)
	}
	b, err := os.ReadFile(fs[0])
	if err != nil {
		t.Fatal(err)
	}
	p := string(b)
	for _, want := range []string{"## Workspace", "its own git worktree", "Commit everything you hand off", "## Goal", "Your goal is your branch", "bsx done"} {
		if !strings.Contains(p, want) {
			t.Fatalf("worker prompt lacks %q:\n%s", want, p)
		}
	}
	for _, not := range []string{"## Goals", "merge it into its base", "bsx prune` removes"} {
		if strings.Contains(p, not) {
			t.Fatalf("worker prompt has the queen's %q:\n%s", not, p)
		}
	}
}

// Stuck states are printed with the next command (Rule G).
func TestStatusSaysWhatIsStuck(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	r := x.keeper("status")
	must(t, r, 0, "stuck: toy-1: ended with its goal incomplete → bsx prune completes it once --goal branch holds")
	if cell(r.out, "toy-1", "STATE") != "ended --incomplete" {
		t.Fatalf("status:\n%s", r)
	}
}

// While a completion check can't run (gh offline), --incomplete still ends the worker,
// with a warning: never a dead end (motif 5).
func TestEndIncompleteIsTheWayOutWhileGitHubIsDown(t *testing.T) {
	x := newToy(t)
	x.git(x.repo, "remote", "add", "origin", "https://github.com/x/toy.git")
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.gh.Offline = true
	must(t, x.queen("end", "toy-1"), 6, "next: bsx end toy-1 --incomplete")
	must(t, x.queen("end", "--incomplete", "toy-1"), 0, "ending it --incomplete", "ended toy-1")
}

// A goal whose prune rule passes at once (verdict) still never loses a branch's commits:
// prune removes the clean worktree and keeps the unmerged branch, saying why.
func TestPruneKeepsAnUnmergedBranchOfAVerdictGoal(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "verdict")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done", "--answer", "yes", "--report", "fine"), 0)
	must(t, x.queen("end", "toy-1"), 0)
	must(t, x.queen("prune"), 3, "removed "+sp.Workspace.Dir+" (toy-1)", "kept branch toy-1 (toy-1): branch toy-1: toy-1 has work not on main")
	if x.git(x.repo, "branch", "--list", "toy-1") == "" {
		t.Fatal("prune deleted an unmerged branch")
	}
}

// A discard is the keeper's recorded word: prune removes the workspace and deletes the
// unmerged branch.
func TestPruneDeletesADiscardedBranch(t *testing.T) {
	x := newToy(t)
	ctx := context.Background()
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	lb, err := logbook.Open(ctx, x.logbook)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lb.RecordGoal(ctx, sp.ID, sp.Name, logbook.GoalChange{By: "keeper", Discard: true}); err != nil {
		t.Fatal(err)
	}
	lb.Close()
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir+" and branch toy-1")
}

// A spawn from before goals recorded the repo's path as its base (the pilot's v1): its
// branch is proven merged against the default branch, so prune removes it rather than
// keeping it as "couldn't check" forever (bees-12.41's class).
func TestALegacySpawnPrunesAgainstTheDefaultBranch(t *testing.T) {
	x := newToy(t)
	dir := filepath.Join(filepath.Dir(x.repo), "toy-WT", "toy-9")
	x.git(x.repo, "worktree", "add", "-q", "-b", "toy-9", dir)
	x.commit(dir, "old.txt")
	x.git(x.repo, "merge", "-q", "toy-9")
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
	must(t, x.queen("prune"), 0, "removed "+dir+" and branch toy-9")
}

// A changed path with a leading space: git's -z name list is read raw, never trimmed, so
// the path compared is the branch's own and the branch is not "proven merged".
func TestAPathWithALeadingSpaceIsNotProvenMerged(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, " lead")
	must(t, x.worker("toy-1", 1, "done"), 0)
	must(t, x.queen("end", "toy-1"), 9, "toy-1 has work not on main")
}

// A stacked base merged and deleted: the branch's work is proven on the default branch,
// never "can't check" forever (motif 5).
func TestADeletedBaseFallsBackToTheDefaultBranch(t *testing.T) {
	x := newToy(t)
	x.git(x.repo, "switch", "-q", "-c", "stack")
	sp := x.spawn("toy-1", "branch")
	if sp.Workspace.Base != "stack" {
		t.Fatalf("base = %q", sp.Workspace.Base)
	}
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.git(x.repo, "switch", "-q", "main")
	x.git(x.repo, "merge", "-q", "toy-1")
	x.git(x.repo, "branch", "-q", "-D", "stack")
	must(t, x.queen("end", "toy-1"), 0, "ended toy-1")
	must(t, x.queen("prune"), 0, "and branch toy-1")
}

// A failed tag takes back the worktree and the branch herdr made: a second spawn starts
// clean (motif 8).
func TestAFailedTagLeavesNoWorktreeOrBranch(t *testing.T) {
	x := newToy(t)
	x.bead("toy-1", "x")
	x.herdr.RefuseNext = "workspace report-metadata"
	must(t, x.queen("spawn", "--goal", "branch", "toy-1"), 1, "couldn't tag its workspace; nothing was recorded")
	if out := x.git(x.repo, "branch", "--list", "toy-1"); out != "" {
		t.Fatalf("branch left: %q", out)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(x.repo), "toy-WT", "toy-1")); !os.IsNotExist(err) {
		t.Fatalf("worktree left: %v", err)
	}
	x.spawn("toy-1", "branch")
}

// A workspace kind set to one not built yet is refused, saying where it was set.
func TestSpawnRefusesAKindNotBuiltYet(t *testing.T) {
	x := newToy(t)
	x.bead("toy-1", "x")
	b, err := os.ReadFile(filepath.Join(x.config, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	x.write(filepath.Join(x.config, "config.toml"), string(b)+"[workspace]\nkind = \"none\"\n")
	must(t, x.queen("spawn", "--goal", "branch", "toy-1"), 2, "workspace kind none (workspace.kind", "bees-13.10")
}

// Prune removes only a directory that is still a worktree of its repo: one remade by
// hand is kept, named; a directory it can't stat is "couldn't check" (exit 4).
func TestPruneKeepsADirThatIsNoLongerAWorktree(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "branch")
	x.commit(sp.Workspace.Dir, "work.txt")
	must(t, x.worker("toy-1", 1, "done"), 0)
	x.git(x.repo, "merge", "-q", "toy-1")
	must(t, x.queen("end", "toy-1"), 0)
	parent := filepath.Dir(sp.Workspace.Dir)
	if err := os.Chmod(parent, 0); err != nil {
		t.Fatal(err)
	}
	r := x.queen("prune")
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	must(t, r, 4, "can't tell whether its directory exists")
	x.git(x.repo, "worktree", "remove", sp.Workspace.Dir)
	if err := os.MkdirAll(sp.Workspace.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	must(t, x.queen("prune"), 3, "kept "+sp.Workspace.Dir, "it is no longer a worktree of ") // the repo, resolved on macOS
}
