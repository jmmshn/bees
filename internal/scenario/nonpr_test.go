package scenario_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/port"
)

// The goals that end in no PR (bees-13.7): each completes at handoff, says where its work
// went in the queen's inbox, and prunes right after bs end.

// A verdict needs --answer yes|no and a report; the queen's inbox shows both.
func TestAVerdictReachesTheQueensInbox(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "verdict")
	must(t, x.worker("toy-1", 1, "done", "--report", "it holds"), 7, "--answer yes|no is missing")
	must(t, x.worker("toy-1", 2, "done", "--answer", "YES", "--report", "it holds"), 7, `--answer must be yes or no (got "YES")`)
	must(t, x.worker("toy-1", 3, "done", "--answer", "yes", "--report", "-"), 2, "stdin was empty")
	must(t, x.worker("toy-1", 4, "done", "--answer", "no"), 7, "--report <text>|- is missing")
	must(t, x.worker("toy-1", 5, "done", "--answer", "no", "--report", "  "), 7, "--report <text>|- is missing")
	must(t, x.worker("toy-1", 6, "done", "--answer", "no", "--report", "the cache is never read"), 0)
	must(t, x.queen("inbox"), 0, "done: toy-1 answer no", "the cache is never read")
	must(t, x.queen("end", "toy-1"), 0)
	x.write(filepath.Join(sp.Workspace.Dir, "verdict.md"), "draft")
	must(t, x.queen("prune"), 3, "kept", "verdict.md")
	if err := os.Remove(filepath.Join(sp.Workspace.Dir, "verdict.md")); err != nil {
		t.Fatal(err)
	}
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir)
}

// Only a verdict takes --answer or --report: a note goal's report goes in the bead, and
// any other goal's words go to the queen as mail (Rule G).
func TestOnlyAVerdictTakesAReport(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "note")
	must(t, x.worker("toy-1", 1, "done", "--report", "see scratch/report.md"), 2, "takes no --answer or --report", "bd update toy-1 --append-notes")
	x.spawn("toy-2", "branch")
	must(t, x.worker("toy-2", 1, "done", "--answer", "yes"), 2, "takes no --answer or --report", "bs send queen-toy")
}

// A note goal needs the bead's notes or design changed since spawn: other fields, a
// label or status among them, aren't hashed (the fake has none; the live test changed a
// label). A report left in the worktree keeps it at prune, named.
func TestANoteGoalNeedsTheBeadsNotesChanged(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "note")
	x.write(filepath.Join(sp.Workspace.Dir, "report.md"), "the findings")
	must(t, x.worker("toy-1", 1, "done"), 7, "note_changed", "as they were at spawn", "bd update toy-1 --append-notes")
	x.beads.Add(port.Bead{ID: "toy-1", Title: "work on toy-1", Description: "do work on toy-1", Notes: "the findings"})
	must(t, x.worker("toy-1", 2, "done"), 0, "notes or design changed: bd show toy-1")
	must(t, x.queen("inbox"), 0, "done: toy-1", "bd show toy-1")
	must(t, x.queen("end", "toy-1"), 0)
	must(t, x.queen("prune"), 3, "kept", "report.md")
}

// A beads goal needs a bead created under the bead since spawn; one there before doesn't
// count. The queen's inbox names the new ones.
func TestABeadsGoalNeedsANewChild(t *testing.T) {
	x := newToy(t)
	x.beads.Add(port.Bead{ID: "toy-1.1", Title: "older", Parent: "toy-1"})
	sp := x.spawn("toy-1", "beads")
	must(t, x.worker("toy-1", 1, "done"), 7, "bead_created", "no bead was created under toy-1 since spawn")
	x.beads.Add(port.Bead{ID: "toy-1.2", Title: "split", Parent: "toy-1"})
	must(t, x.worker("toy-1", 2, "done"), 0)
	must(t, x.queen("inbox"), 0, "created under toy-1: toy-1.2")
	if ld, ok, _ := x.open().LandedOf(context.Background(), sp.ID); !ok || ld.By != "done" {
		t.Fatalf("landed = %+v, %v", ld, ok)
	}
	must(t, x.queen("end", "toy-1"), 0)
	must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir)
}

// A goal amended to note checks against the bead as spawn froze it, so notes written
// before the amend count.
func TestAnAmendToNoteChecksAgainstTheSpawn(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "branch")
	x.beads.Add(port.Bead{ID: "toy-1", Title: "work on toy-1", Notes: "written before the amend"})
	must(t, x.keeper("goal", "toy-1", "note"), 0)
	must(t, x.worker("toy-1", 1, "done"), 0, "notes or design changed")
}

// A goal change is about one spawn: once it has ended, a later spawn of the same name
// never reads it (live test of bees-13.7: a respawned worker read the old discard and
// stopped).
func TestAGoalMessageStaysWithItsSpawn(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	must(t, x.keeper("goal", "toy-1", "discard"), 0)
	must(t, x.queen("end", "toy-1"), 0)
	must(t, x.queen("prune"), 0)
	x.spawn("toy-1", "verdict")
	r := x.worker("toy-1", 1, "inbox")
	must(t, r, 0, "You own toy-1")
	if strings.Contains(r.String(), "discarded") {
		t.Fatalf("the new spawn reads the old spawn's discard:\n%s", r)
	}
}

// A report in an ignored scratch/ is hidden from git status: prune still keeps its
// worktree and names it, so nobody loses it unseen (five pilot reports were, bees-13.7).
// Ignored build output under prune.ignore (default bin/) goes with the worktree.
func TestPruneKeepsIgnoredFilesButBuildOutput(t *testing.T) {
	x := newToy(t)
	x.write(filepath.Join(x.repo, ".git", "info", "exclude"), "/scratch/\n/bin/\n")
	for _, n := range []string{"toy-1", "toy-2"} {
		sp := x.spawn(n, "verdict")
		if err := os.MkdirAll(filepath.Join(sp.Workspace.Dir, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		x.write(filepath.Join(sp.Workspace.Dir, "bin", "bs"), "a build")
		must(t, x.worker(n, 1, "done", "--answer", "yes", "--report", "see scratch/report.md"), 0)
		must(t, x.queen("end", n), 0)
	}
	kept, _, _ := x.open().LatestSpawn(context.Background(), "toy-2")
	if err := os.MkdirAll(filepath.Join(kept.Workspace.Dir, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	x.write(filepath.Join(kept.Workspace.Dir, "scratch", "report.md"), "the findings")
	r := x.queen("prune")
	must(t, r, 3, "removed "+filepath.Join(x.repo+"-WT", "toy-1"), "kept", "ignored files", "scratch/")
	if strings.Contains(r.String(), "bin/") {
		t.Fatalf("prune named the build output prune.ignore lets go:\n%s", r)
	}
	if err := os.RemoveAll(filepath.Join(kept.Workspace.Dir, "scratch")); err != nil {
		t.Fatal(err)
	}
	must(t, x.queen("prune"), 0, "removed "+kept.Workspace.Dir)
}
