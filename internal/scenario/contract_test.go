package scenario_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/cli"
	"github.com/jmmshn/bees/internal/fake"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/proc"
)

// The contract card (bees-13.68, rebuild ticket T0): every case of the keeper's
// 2026-10-22 post-swap pilot (.claude/skills/live-test/CONTRACT.md) as scenario tests over
// the fakes, rewritten for ADR-0010 Amendment 4. A case of behavior not built yet is an
// xfail naming the rebuild ticket that builds it (bees-13.67 §4): T1 identity and
// permissions, T2 cross-project send, T3 find the change, T4 one goal per spawn, T5
// stacked pr= and narrow push=, T6 bs posts the review, T7 #118's end and stuck hints.
// Each case is a subtest named by its case id; a group's cases share one toy, in order.

// card is one group of the card on one toy.
type card struct {
	t     *testing.T
	x     *toy
	steps map[string]int // each session's script steps so far
}

func newCard(t *testing.T) *card {
	return &card{t: t, x: newToy(t), steps: map[string]int{}}
}

// c runs case id; ticket "" is a case that holds today, else the ticket it waits for.
func (k *card) c(id, ticket string, f func(t *testing.T, x *toy)) {
	k.t.Helper()
	k.t.Run(id, func(t *testing.T) {
		if ticket != "" {
			xfail(t, ticket)
		}
		f(t, k.x.with(t))
	})
}

// w runs bs args in session name's pane, a child of its harness, as its next step.
func (k *card) w(x *toy, name string, args ...string) result {
	x.t.Helper()
	k.steps[name]++
	return x.worker(name, k.steps[name], args...)
}

// step runs one non-bs step (cd, child, kill) in session name's pane and waits for it.
func (k *card) step(x *toy, name string, step ...string) {
	x.t.Helper()
	k.steps[name]++
	x.harness.Step(x.t, name, step...)
	x.harness.Await(x.t, name, k.steps[name])
}

// with is x failing through t: a subtest's toy.
func (x *toy) with(t testing.TB) *toy {
	y := *x
	y.t = tb(t)
	return &y
}

// sqlOne is the first column of query's first row in the toy's logbook ("" for none).
func (x *toy) sqlOne(query string, args ...any) string {
	x.t.Helper()
	db, err := sql.Open("sqlite", x.logbook+"?mode=ro")
	if err != nil {
		x.t.Fatal(err)
	}
	defer db.Close()
	var s sql.NullString
	if err := db.QueryRow(query, args...).Scan(&s); err != nil && err != sql.ErrNoRows {
		x.t.Fatal(err)
	}
	return s.String
}

// rawEntry appends an entry to the toy's logbook as an older bs wrote it: data is its
// JSON as is, so a test can hold a shape today's types no longer make (a done's prs, a
// mixed goal).
func (x *toy) rawEntry(kind, actor, subject string, ref logbook.ID, data string) {
	x.t.Helper()
	db, err := sql.Open("sqlite", x.logbook)
	if err != nil {
		x.t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	id := fmt.Sprintf("%013x%013x", now.UnixMilli(), now.UnixNano()&0xfffffffffffff)
	if _, err := db.Exec(`INSERT INTO entries (id, ts, kind, actor, subject, ref, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, now.Format("2006-01-02T15:04:05.000Z07:00"), kind, actor, subject, string(ref), data); err != nil {
		x.t.Fatal(err)
	}
}

func lacks(t testing.TB, r result, not ...string) {
	t.Helper()
	for _, n := range not {
		if strings.Contains(r.String(), n) {
			fatalf(t, "output has %q:\n%s", n, r)
		}
	}
}

// --- offline tier ------------------------------------------------------------------

// Spawn refusals, exit 2 each, recording nothing. Amendment 4: one --goal per spawn.
func TestContractSpawnRefusals(t *testing.T) {
	k := newCard(t)
	k.x.start()
	k.x.bead("toy-1", "refusals")
	for _, c := range []struct {
		id, ticket string
		args       []string
		want       []string
	}{
		{"spawn-no-goal", "", nil, []string{"goal is required"}},
		{"spawn-pr-merged", "", []string{"--goal", "pr:merged"}, []string{`pr has no state "merged"`}},
		{"spawn-review-bare", "", []string{"--goal", "review"}, []string{"review needs a target"}},
		{"spawn-bad-value", "", []string{"--goal", "verdict=x"}, []string{"verdict takes no target"}},
		{"spawn-push-dash", "", []string{"--goal", "push=-x"}, []string{"a target can't start with -"}},
		{"spawn-review-no-remote", "", []string{"--goal", "review=https://github.com/cli/cli/pull/1"}, []string{"belongs to no github repo"}},
		// Last: until T4 the two-goal spawn is accepted, and spawns toy-1.
		{"spawn-two-bad", "T4", []string{"--goal", "verdict=x", "--goal", "nope"}, []string{"one goal per spawn"}},
		{"spawn-two-goals", "T4", []string{"--goal", "verdict", "--goal", "note"}, []string{"one goal per spawn"}},
	} {
		k.c(c.id, c.ticket, func(t *testing.T, x *toy) {
			must(t, x.queen(append(append([]string{"spawn"}, c.args...), "toy-1")...), 2, c.want...)
			if _, ok, _ := x.open().LatestSpawn(context.Background(), "toy-1"); ok {
				fatalf(t, "a refused spawn was recorded")
			}
		})
	}
	k.c("spawn-unknown-bead", "", func(t *testing.T, x *toy) {
		must(t, x.queen("spawn", "--goal", "verdict", "toy-99"), 2, "no bead toy-99")
	})
}

// A verdict worker V from first done to after its end.
func TestContractVerdict(t *testing.T) {
	k := newCard(t)
	sp := k.x.spawn("toy-1", "verdict")
	V := "toy-1"
	k.c("verdict-no-answer", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, V, "done"), 7, "verdict_given: --answer yes|no is missing")
	})
	k.c("verdict-maybe", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, V, "done", "--answer", "maybe", "--report", "x"), 7, `must be yes or no (got "maybe")`)
	})
	k.c("verdict-blank-report", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, V, "done", "--answer", "yes", "--report", "   "), 7, "report <text>|- is missing")
	})
	k.c("verdict-no-report", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, V, "done", "--answer", "yes"), 7, "report <text>|- is missing")
	})
	// Amendment 4: --pr is gone, from every goal.
	k.c("verdict-pr", "T3", func(t *testing.T, x *toy) {
		must(t, k.w(x, V, "done", "--pr", "https://github.com/a/b/pull/1", "--answer", "yes", "--report", "x"), 2, "flag provided but not defined: -pr")
	})
	k.c("verdict-end-early", "", func(t *testing.T, x *toy) {
		must(t, x.queen("end", V), 9, "next: bs end --incomplete "+V)
	})
	k.c("verdict-stdin", "", func(t *testing.T, x *toy) {
		must(t, x.shellRun(x.repo, x.env, "the report from stdin\n", "done", "--as", V, "--answer", "no", "--report", "-"), 0, "answer no", "the report from stdin")
	})
	k.c("verdict-done-after-landed", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, V, "done", "--answer", "yes", "--report", "changed"), 2)
	})
	k.c("verdict-end", "", func(t *testing.T, x *toy) { must(t, x.queen("end", V), 0, "ended "+V) })
	k.c("verdict-end-again", "", func(t *testing.T, x *toy) { must(t, x.queen("end", V), 2, "already ended") })
	k.c("verdict-done-after-end", "", func(t *testing.T, x *toy) {
		must(t, x.keeper("done", "--as", V, "--answer", "yes", "--report", "late"), 2, "no open spawn for "+V)
	})
	k.c("end-closes-workspace", "", func(t *testing.T, x *toy) {
		out, err := x.herdr.Run(context.Background(), "", "herdr", "--session", x.session, "workspace", "list")
		if err != nil || sp.Herdr.Workspace == "" || strings.Contains(string(out), `"`+sp.Herdr.Workspace+`"`) {
			fatalf(t, "workspace %q after end: %s %v", sp.Herdr.Workspace, out, err)
		}
	})
	k.c("end-stops-harness", "", func(t *testing.T, x *toy) {
		st, ok, err := x.open().LatestStart(context.Background(), sp.ID)
		if err != nil || !ok {
			fatalf(t, "no start: %v", err)
		}
		if alive, _ := x.herdr.Alive(st.Process); alive {
			fatalf(t, "harness %d still runs after end", st.PID)
		}
	})
}

// A note worker N: its bead's notes or design changed since spawn.
func TestContractNote(t *testing.T) {
	k := newCard(t)
	k.x.spawn("toy-1", "note")
	N := "toy-1"
	set := func(x *toy, notes, design string) {
		x.beads.Add(port.Bead{ID: N, Title: "work on " + N, Description: "do work on " + N, Notes: notes, Design: design})
	}
	k.c("note-unchanged", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, N, "done"), 7, "note_changed: "+N+"'s notes and design are as they were")
	})
	k.c("note-answer", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, N, "done", "--answer", "yes", "--report", "x"), 2, "takes no --answer", "next: bd update "+N)
	})
	k.c("note-changed", "", func(t *testing.T, x *toy) {
		set(x, "changed", "")
		must(t, x.keeper("check", N), 0, "ok   note_changed")
	})
	k.c("note-reset", "", func(t *testing.T, x *toy) {
		set(x, "", "") // its spawn-time text: none
		must(t, x.keeper("check", N), 7, "FAIL note_changed")
	})
	k.c("note-design", "", func(t *testing.T, x *toy) {
		set(x, "", "a design")
		must(t, x.keeper("check", N), 0, "ok   note_changed")
	})
}

// A beads worker B: a bead created under its own since spawn.
func TestContractBeads(t *testing.T) {
	k := newCard(t)
	k.x.spawn("toy-1", "beads")
	k.x.bead("toy-2", "elsewhere")
	B := "toy-1"
	k.c("beads-none", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, B, "done"), 7, "bead_created: no bead was created under "+B)
	})
	k.c("beads-elsewhere", "", func(t *testing.T, x *toy) {
		x.beads.Add(port.Bead{ID: "toy-2.1", Title: "a child elsewhere", Parent: "toy-2"})
		must(t, x.keeper("check", B), 7, "FAIL bead_created")
	})
	k.c("beads-own", "", func(t *testing.T, x *toy) {
		x.beads.Add(port.Bead{ID: B + ".1", Title: "a child here", Parent: B})
		must(t, x.keeper("check", B), 0, "ok   bead_created: created under "+B)
	})
}

// A branch worker BR: a clean branch ahead of main.
func TestContractBranch(t *testing.T) {
	k := newCard(t)
	k.x.write(filepath.Join(k.x.repo, ".gitignore"), "bin/\nscratch/\n")
	k.x.git(k.x.repo, "add", ".gitignore")
	k.x.git(k.x.repo, "commit", "-qm", "ignores")
	sp := k.x.spawn("toy-1", "branch")
	BR, bw := "toy-1", sp.Workspace.Dir
	k.c("branch-no-commit", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, BR, "done"), 7, "branch_ahead: "+BR+" has no commit that main lacks")
	})
	k.c("branch-untracked", "", func(t *testing.T, x *toy) {
		x.commit(bw, "f.txt")
		x.write(filepath.Join(bw, "untracked.txt"), "u")
		must(t, x.keeper("check", BR), 7, "FAIL clean", "untracked.txt")
	})
	k.c("branch-ignored-bin", "", func(t *testing.T, x *toy) {
		os.Remove(filepath.Join(bw, "untracked.txt"))
		if err := os.MkdirAll(filepath.Join(bw, "bin"), 0o700); err != nil {
			fatalf(t, "%v", err)
		}
		x.write(filepath.Join(bw, "bin", "bs"), "b")
		must(t, x.keeper("check", BR), 0, "ok   clean")
	})
	k.c("branch-modified", "", func(t *testing.T, x *toy) {
		x.write(filepath.Join(bw, "f.txt"), "mod\n")
		must(t, x.keeper("check", BR), 7, "FAIL clean", "f.txt")
	})
	k.c("branch-pass", "", func(t *testing.T, x *toy) {
		x.git(bw, "checkout", "-q", "f.txt")
		must(t, k.w(x, BR, "done"), 0, "branch_ahead: 1 commit ahead of main")
	})
	k.c("branch-end-early", "", func(t *testing.T, x *toy) {
		must(t, x.queen("end", BR), 9, "branch_merged", "has work not on main")
	})
}

// The background gate (worker G): a child of its harness the start didn't see.
func TestContractGate(t *testing.T) {
	k := newCard(t)
	sp := k.x.spawn("toy-1", "verdict")
	G := "toy-1"
	k.steps[G]++
	k.x.harness.Step(t, G, "child", "server")
	kid := k.x.harness.Await(t, G, k.steps[G]).PID
	k.c("gate-check", "", func(t *testing.T, x *toy) {
		must(t, x.keeper("check", G), 7, "FAIL no_background", "background process")
	})
	k.c("gate-done", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, G, "done", "--answer", "yes", "--report", "r"), 5, "still runs background work", fmt.Sprintf("pid %d", kid))
	})
	k.c("gate-end-incomplete", "", func(t *testing.T, x *toy) {
		must(t, x.queen("end", "--incomplete", G), 5, "still runs background work")
	})
	// #118 (bees-13.52): the way out of --incomplete keeps --incomplete.
	k.c("gate-end-incomplete-next", "T7", func(t *testing.T, x *toy) {
		must(t, x.queen("end", "--incomplete", G), 5, "next: bs end --incomplete --force "+G)
	})
	k.c("gate-force-next", "T7", func(t *testing.T, x *toy) {
		r := x.queen("end", "--force", G) // today's next: above, followed
		must(t, r, 9, "next: bs end --incomplete --force "+G)
	})
	k.c("gate-done-force", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, G, "done", "--force", "--answer", "yes", "--report", "r"), 0)
	})
	k.c("gate-waived", "", func(t *testing.T, x *toy) {
		d, ok, err := x.open().LatestDone(context.Background(), sp.ID)
		if err != nil || !ok || !slices.ContainsFunc(d.Checks, func(c logbook.CheckResult) bool { return c.Waived }) ||
			!slices.ContainsFunc(d.Background, func(b proc.Background) bool { return b.PID == kid }) {
			fatalf(t, "done %+v %v %v: want the gate waived, naming %d", d, ok, err, kid)
		}
	})
	k.c("gate-end", "", func(t *testing.T, x *toy) {
		must(t, x.queen("end", G), 5, "still runs background work", "next: bs end --force "+G)
	})
	k.c("gate-end-force", "", func(t *testing.T, x *toy) { must(t, x.queen("end", "--force", G), 0, "ended "+G) })
	// gate-sleep-stopped stays in the live run (contract.sh): the background job dies with
	// the real pane's pty, which the fake herdr doesn't model.
}

// Amend, waive, discard (workers A and D; M has landed). Amendment 4's table: the queen
// amends and waives but never discards; --as from the keeper's shell takes the named
// session's permissions.
func TestContractAmendWaiveDiscard(t *testing.T) {
	k := newCard(t)
	k.x.github()
	A, D, M := "toy-1", "toy-2", "toy-3"
	spA := k.x.spawn(A, "verdict")
	spD := k.x.spawn(D, "branch")
	k.x.spawn(M, "verdict")
	must(t, k.w(k.x, M, "done", "--answer", "yes", "--report", "ok"), 0)
	lastGoal := func(x *toy, sp logbook.Spawn) string {
		return x.sqlOne(`SELECT data FROM entries WHERE kind = 'goal' AND ref = ? ORDER BY rowid DESC LIMIT 1`, string(sp.ID))
	}
	k.c("amend-other-repo", "", func(t *testing.T, x *toy) {
		must(t, x.queen("goal", A, "review=https://github.com/cli/cli/pull/1"), 2, "that is a change of cli/cli")
	})
	k.c("amend-unknown", "", func(t *testing.T, x *toy) { must(t, x.queen("goal", A, "nope"), 2, `no goal type "nope"`) })
	k.c("amend-waive-unrun", "", func(t *testing.T, x *toy) {
		must(t, x.queen("goal", A, "--waive", "pr_pushed"), 2, "--waive pr_pushed: --goal verdict runs no such check")
	})
	k.c("amend-pr-draft", "", func(t *testing.T, x *toy) { must(t, x.queen("goal", A, "pr:draft"), 0, "--goal pr:draft") })
	k.c("amend-mailed", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, A, "inbox"), 0, "changed your goal: it is now --goal pr:draft")
	})
	k.c("amend-waive", "", func(t *testing.T, x *toy) {
		must(t, x.queen("goal", A, "--waive", "pr_pushed"), 0, "waived: pr_pushed")
	})
	k.c("amend-two-values", "T4", func(t *testing.T, x *toy) {
		must(t, x.queen("goal", A, "verdict", "note"), 2, "one goal per spawn")
	})
	k.c("amend-as-worker", "T1", func(t *testing.T, x *toy) {
		must(t, x.keeper("goal", "--as", A, A, "verdict"), 2, A, "worker")
	})
	k.c("amend-after-landed", "", func(t *testing.T, x *toy) { must(t, x.queen("goal", M, "note"), 2, "complete (landed)") })
	k.c("discard-as-queen", "T1", func(t *testing.T, x *toy) {
		must(t, x.queen("goal", A, "discard"), 2, "queen-toy", "only the keeper discards")
	})
	k.c("discard-as-queen-by", "T1", func(t *testing.T, x *toy) {
		if g := lastGoal(x, spA); strings.Contains(g, `"discard":true`) {
			fatalf(t, "the queen's discard was recorded: %s", g)
		}
	})
	k.c("discard-keeper", "", func(t *testing.T, x *toy) { must(t, x.keeper("goal", D, "discard"), 0, "discarded") })
	k.c("discard-keeper-by", "", func(t *testing.T, x *toy) {
		if g := lastGoal(x, spD); !strings.Contains(g, `"by":"keeper"`) || !strings.Contains(g, `"discard":true`) {
			fatalf(t, "D's goal entry: %s", g)
		}
	})
}

// End and prune: N (landed) keeps an ignored scratch/report.md; B (landed) has only an
// ignored bin/bs; BR (unlanded) an ignored scratch/keep.md; D (discarded) a commit on no
// other branch. Then BR is spawned again on its kept worktree and branch.
func TestContractEndAndPrune(t *testing.T) {
	k := newCard(t)
	k.x.write(filepath.Join(k.x.repo, ".gitignore"), "bin/\nscratch/\n")
	k.x.git(k.x.repo, "add", ".gitignore")
	k.x.git(k.x.repo, "commit", "-qm", "ignores")
	N, B, BR, D := "toy-1", "toy-2", "toy-3", "toy-4"
	wt := map[string]string{}
	for name, g := range map[string]string{N: "verdict", B: "verdict", BR: "branch", D: "branch"} {
		wt[name] = k.x.spawn(name, g).Workspace.Dir
	}
	for _, f := range []string{wt[N] + "/scratch/report.md", wt[B] + "/bin/bs", wt[BR] + "/scratch/keep.md"} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		k.x.write(f, "kept\n")
	}
	k.x.commit(wt[BR], "f.txt")
	k.x.commit(wt[D], "d.txt")
	must(t, k.w(k.x, N, "done", "--answer", "yes", "--report", "r"), 0)
	must(t, k.w(k.x, B, "done", "--answer", "yes", "--report", "r"), 0)
	must(t, k.x.keeper("goal", D, "discard"), 0)
	for _, n := range []string{N, B, D} {
		k.c("end-"+n, "", func(t *testing.T, x *toy) { must(t, x.queen("end", n), 0, "ended "+n) })
	}
	k.c("end-incomplete", "", func(t *testing.T, x *toy) { must(t, x.queen("end", "--incomplete", BR), 0, "ended "+BR) })
	k.c("end-incomplete-keeps", "", func(t *testing.T, x *toy) {
		for _, f := range []string{"f.txt", "scratch/keep.md"} {
			if _, err := os.Stat(filepath.Join(wt[BR], f)); err != nil {
				fatalf(t, "%s: %v", f, err)
			}
		}
		if !x.hasBranch(BR) {
			fatalf(t, "branch %s is gone", BR)
		}
	})
	var pr result
	k.c("prune-exit", "", func(t *testing.T, x *toy) { pr = x.queen("prune"); must(t, pr, 3, "kept some workspaces") })
	k.c("prune-keeps-unmerged-branch", "", func(t *testing.T, x *toy) { must(t, pr, 3, "kept "+wt[BR], "has work not on main") })
	k.c("prune-keeps-scratch", "", func(t *testing.T, x *toy) { must(t, pr, 3, "kept "+wt[N], "scratch/report.md") })
	k.c("prune-removes-bin-only", "", func(t *testing.T, x *toy) { must(t, pr, 3, "removed "+wt[B]+" and branch") })
	k.c("prune-removes-discarded", "", func(t *testing.T, x *toy) { must(t, pr, 3, "removed "+wt[D]+" and branch") })
	k.c("prune-removed-discarded-commit", "", func(t *testing.T, x *toy) {
		if x.hasBranch(D) {
			fatalf(t, "the discarded branch %s is still there", D)
		}
	})
	k.c("respawn-kept", "", func(t *testing.T, x *toy) {
		must(t, x.queen("spawn", "--goal", "branch", BR), 0, "warning: "+BR+": on its kept worktree and branch")
	})
	k.c("respawn-commit-counts", "", func(t *testing.T, x *toy) {
		must(t, x.keeper("check", BR), 0, "ok   branch_ahead: 1 commit ahead of main")
	})
}

// Resume (worker I): its harness killed, status says stuck, and a spawn with its goal
// resumes it in its pane.
func TestContractResume(t *testing.T) {
	k := newCard(t)
	sp := k.x.spawn("toy-1", "verdict")
	I := "toy-1"
	k.x.kill(I)
	k.c("resume-stuck", "", func(t *testing.T, x *toy) {
		must(t, x.keeper("status"), 0, "stuck: "+I+": its process is gone", "resumes it")
	})
	k.c("resume-other-goal", "", func(t *testing.T, x *toy) {
		must(t, x.queen("spawn", "--goal", "note", I), 2, "next: bs goal "+I)
	})
	k.c("resume", "", func(t *testing.T, x *toy) {
		must(t, x.queen("spawn", "--goal", "verdict", I), 0, "resumed "+I+" in pane "+sp.Herdr.Pane)
	})
}

// Identity, from worker I's own tree (Amendment 4 Identity, Permissions): a worker hands
// off and checks its own work and changes nothing else; --as is the keeper's alone.
func TestContractIdentity(t *testing.T) {
	k := newCard(t)
	I, S1, S2 := "toy-1", "toy-2", "toy-3"
	spI := k.x.spawn(I, "verdict")
	k.x.spawn(S1, "verdict")
	k.x.spawn(S2, "verdict")
	k.c("ident-goal-self", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, I, "goal", I, "note"), 2, "a worker never changes a goal")
	})
	k.c("ident-goal", "", func(t *testing.T, x *toy) { must(t, k.w(x, I, "goal", S1, "note"), 2) })
	k.c("ident-bs-project", "", func(t *testing.T, x *toy) {
		k.step(x, I, "env", "BS_PROJECT=other")
		r := k.w(x, I, "status")
		k.step(x, I, "env", "BS_PROJECT=")
		must(t, r, 0, "warning: ignoring BS_PROJECT")
	})
	k.c("ident-end-sibling", "T1", func(t *testing.T, x *toy) {
		must(t, k.w(x, I, "end", S1), 2, I+" runs this as a worker; only the queen or the keeper ends a worker")
	})
	k.c("ident-end-incomplete-sibling", "T1", func(t *testing.T, x *toy) {
		must(t, k.w(x, I, "end", "--incomplete", S2), 2, I+" runs this as a worker")
	})
	k.c("ident-prune", "T1", func(t *testing.T, x *toy) { must(t, k.w(x, I, "prune"), 2, I+" runs this as a worker") })
	k.c("ident-as-other", "T1", func(t *testing.T, x *toy) { must(t, k.w(x, I, "status", "--as", S1), 2, "--as") })
	k.c("ident-inbox-as-other", "", func(t *testing.T, x *toy) { must(t, k.w(x, I, "inbox", "--as", S1), 2, "--as") })
	k.c("ident-check-other", "T1", func(t *testing.T, x *toy) { must(t, k.w(x, I, "check", S1), 2, I+" runs this as a worker") })
	k.c("status-first-line-worker", "T1", func(t *testing.T, x *toy) {
		r := k.w(x, I, "status")
		if first, _, _ := strings.Cut(r.out, "\n"); !strings.HasPrefix(first, "as "+I+" (worker of toy, spawned ") || !strings.HasSuffix(first, "· logbook "+x.logbook) {
			fatalf(t, "status's first line %q", first)
		}
	})
	k.c("status-first-line-keeper", "T1", func(t *testing.T, x *toy) {
		r := x.keeper("status")
		if first, _, _ := strings.Cut(r.out, "\n"); first != "as keeper (keeper of toy) · logbook "+x.logbook {
			fatalf(t, "status's first line %q", first)
		}
	})
	// The tripwire: a herdr pane no entry recorded, inside a live spawn's workspace, is
	// that worker, with a warning.
	k.c("ident-tripwire", "T1", func(t *testing.T, x *toy) {
		x.shell(x.session, "stray", spI.Workspace.Dir)
		must(t, k.w(x, "stray", "status"), 0, "as "+I+" (worker of toy", "warning")
		must(t, k.w(x, "stray", "end", S1), 2, I+" runs this as a worker")
	})
	// A residual escape (Amendment 4): a pane outside any recorded tree and workspace
	// reads as the keeper. It documents the escape; it is no fence.
	k.c("ident-outside-reads-keeper", "T1", func(t *testing.T, x *toy) {
		x.shell(x.session, "outside", x.repo)
		must(t, k.w(x, "outside", "status"), 0, "as keeper (keeper of toy)")
	})
}

// --as from the keeper's shell (queen note 2 on bees-13.68): it acts as the named session
// with that session's role, and bs inbox --as peeks.
func TestContractAs(t *testing.T) {
	k := newCard(t)
	W, W2 := "toy-1", "toy-2"
	k.x.spawn(W, "verdict")
	k.x.spawn(W2, "verdict")
	k.c("as-done-simulates-worker", "", func(t *testing.T, x *toy) {
		must(t, x.keeper("done", "--as", W, "--answer", "yes", "--report", "r"), 0, "handed off to queen-toy")
	})
	k.c("as-check-own", "T1", func(t *testing.T, x *toy) {
		must(t, x.keeper("check", "--as", W2, W2), 7, "FAIL verdict_given")
	})
	k.c("as-check-other", "T1", func(t *testing.T, x *toy) {
		must(t, x.keeper("check", "--as", W2, W), 2, W2+" runs this as a worker")
	})
	k.c("as-end-as-worker", "T1", func(t *testing.T, x *toy) {
		must(t, x.keeper("end", "--as", W2, W), 2, W2+" runs this as a worker")
	})
	k.c("as-send-queen-down", "", func(t *testing.T, x *toy) {
		must(t, x.keeper("send", "--as", "queen-toy", W2, "from the queen"), 0, "to "+W2)
		must(t, k.w(x, W2, "inbox"), 0, "from queen-toy", "from the queen")
	})
	k.c("as-inbox-peeks", "T1", func(t *testing.T, x *toy) {
		must(t, x.queen("send", W2, "peek me"), 0)
		must(t, x.keeper("inbox", "--as", W2), 0, "peek me")
		must(t, x.keeper("inbox", "--as", W2), 0, "peek me") // marked nothing read
		must(t, k.w(x, W2, "inbox"), 0, "peek me")
	})
	k.c("as-queen-rig", "", func(t *testing.T, x *toy) { must(t, x.queen("send", W2, "still the queen"), 0, "to "+W2) })
}

// Parallel first spawns on a new project, with a concurrent read-only scan of its
// logbook (bees-13.59): every spawn works and one prints R48's new logbook line.
func TestContractParallelFirstSpawns(t *testing.T) {
	// xfail(t, "T1") [DEBUG-p59]
	x := newToy(t)
	const n = 11
	for i := 1; i <= n; i++ {
		x.bead(fmt.Sprintf("toy-%d", i), "parallel")
	}
	rs := make([]result, n)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	scanErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				scanErr <- nil
				return
			default:
			}
			if lb, err := logbook.OpenReadOnly(context.Background(), x.logbook); err == nil {
				_, err = lb.LatestSpawns(context.Background())
				lb.Close()
				if err != nil {
					scanErr <- err
					return
				}
			}
		}
	}()
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out, errb bytes.Buffer
			code := cli.Run(context.Background(), cli.Env{Vars: x.env, Dir: x.repo, Self: os.Getpid(), Stdin: strings.NewReader("")}, x.ports,
				[]string{"spawn", "--as", "queen-toy", "--goal", "verdict", fmt.Sprintf("toy-%d", i+1)}, &out, &errb)
			rs[i] = result{code, out.String(), errb.String()}
		}()
	}
	wg.Wait()
	close(stop)
	if err := <-scanErr; err != nil {
		fatalf(t, "a read-only scan during the first spawns: %v", err)
	}
	created := 0
	for i, r := range rs {
		if r.code != 0 {
			fatalf(t, "spawn toy-%d: exit %d:\n%s", i+1, r.code, r)
		}
		created += strings.Count(r.String(), "new logbook")
	}
	if created != 1 {
		fatalf(t, "%d new logbook lines, want 1", created)
	}
}

// Cross-project (Amendment 4): a session of toy acting in project other may only message
// queen-other; the message is stored in other's logbook, and other's queen is started
// when she isn't running, once however many sends race (motif 7).
func TestContractCrossProject(t *testing.T) {
	k := newCard(t)
	ctx := context.Background()
	other := filepath.Join(filepath.Dir(k.x.repo), "other")
	k.x.git(filepath.Dir(k.x.repo), "init", "-q", "-b", "main", other)
	k.x.commit(other, "README")
	otherDB := filepath.Join(filepath.Dir(k.x.logbook), "other.db")
	lb, _, err := logbook.Create(ctx, otherDB)
	if err != nil {
		t.Fatal(err)
	}
	lb.Close()
	plain := filepath.Join(filepath.Dir(k.x.repo), "plain")
	k.x.git(filepath.Dir(k.x.repo), "init", "-q", "-b", "main", plain)
	k.x.spawn("toy-1", "verdict")
	k.x.spawn("toy-2", "verdict")
	k.step(k.x, "toy-1", "cd", other)
	k.step(k.x, "toy-2", "cd", other)
	queens := func(x *toy) int {
		ro, err := logbook.OpenReadOnly(ctx, otherDB)
		if err != nil {
			fatalf(x.t, "%v", err)
		}
		defer ro.Close()
		q, ok, err := ro.LatestQueen(ctx, "queen-other")
		if err != nil {
			fatalf(x.t, "%v", err)
		}
		if !ok {
			return 0
		}
		alive, _ := x.herdr.Alive(q.Process)
		if !alive {
			return 0
		}
		return 1
	}
	k.c("foreign-send-starts-queen", "T2", func(t *testing.T, x *toy) {
		must(t, k.w(x, "toy-1", "send", "queen-other", "your API changed?"), 0, "queen-other")
		if queens(x) != 1 {
			fatalf(t, "no running queen-other after a send from toy-1")
		}
		ro, _ := logbook.OpenReadOnly(ctx, otherDB)
		defer ro.Close()
		if ms, err := ro.Unread(ctx, "queen-other"); err != nil || len(ms) != 1 || ms[0].From != "toy-1" || ms[0].FromProject != "toy" {
			fatalf(t, "other's inbox: %+v %v", ms, err)
		}
	})
	k.c("foreign-sends-race-one-queen", "T2", func(t *testing.T, x *toy) {
		for _, w := range []string{"toy-1", "toy-2"} {
			k.steps[w]++
			x.harness.Step(t, w, "bs", "send", "queen-other", "racing "+w)
		}
		for _, w := range []string{"toy-1", "toy-2"} {
			if r := x.harness.Await(t, w, k.steps[w]); r.Code != 0 {
				fatalf(t, "%s's send: exit %d: %s%s", w, r.Code, r.Stdout, r.Stderr)
			}
		}
		db, err := sql.Open("sqlite", otherDB+"?mode=ro")
		if err != nil {
			fatalf(t, "%v", err)
		}
		defer db.Close()
		var q int
		if err := db.QueryRow(`SELECT count(*) FROM entries WHERE kind = 'queen'`).Scan(&q); err != nil || q != 1 {
			fatalf(t, "%d queen entries in other's logbook, want 1 (%v)", q, err)
		}
	})
	k.c("foreign-end", "T2", func(t *testing.T, x *toy) {
		must(t, k.w(x, "toy-1", "end", "other-1"), 2, "you are toy-1 of toy; message queen-other")
	})
	k.c("foreign-spawn", "T2", func(t *testing.T, x *toy) {
		must(t, k.w(x, "toy-1", "spawn", "--goal", "verdict", "other-1"), 2, "you are toy-1 of toy; message queen-other")
	})
	k.c("foreign-status", "", func(t *testing.T, x *toy) { must(t, k.w(x, "toy-1", "status"), 0) })
	k.c("send-no-logbook", "T2", func(t *testing.T, x *toy) {
		k.step(x, "toy-1", "cd", plain)
		r := k.w(x, "toy-1", "send", "queen-plain", "hi")
		k.step(x, "toy-1", "cd", other)
		must(t, r, 2, "no bees project")
		if _, err := os.Stat(filepath.Join(filepath.Dir(x.logbook), "plain.db")); err == nil {
			fatalf(t, "a send made plain's logbook")
		}
	})
}

// Replies route queen to queen: queen-other answers queen-toy, never toy's worker.
func TestContractQueenToQueen(t *testing.T) {
	k := newCard(t)
	other := filepath.Join(filepath.Dir(k.x.repo), "other")
	k.x.git(filepath.Dir(k.x.repo), "init", "-q", "-b", "main", other)
	k.x.startQueen()
	k.x.startQueenOf("other", other)
	k.x.spawn("toy-1", "verdict")
	k.c("queen-reply", "", func(t *testing.T, x *toy) {
		must(t, k.w(x, "queen-other", "send", "--project", "toy", "queen-toy", "hello back"), 0, "rung queen-toy")
	})
	k.c("queen-reply-to-worker", "T2", func(t *testing.T, x *toy) {
		must(t, k.w(x, "queen-other", "send", "--project", "toy", "toy-1", "direct"), 2, "message queen-toy")
	})
}

// --- GitHub tier, on the fake gh --------------------------------------------------

// gcase runs case id on a toy of its own whose origin is the fake GitHub.
func gcase(t *testing.T, id, ticket string, f func(t *testing.T, x *toy, b ghBackend)) {
	t.Helper()
	t.Run(id, func(t *testing.T) {
		if ticket != "" {
			xfail(t, ticket)
		}
		x := newToy(t)
		x.github()
		f(t, x, ghBackend{})
	})
}

// T3 (Amendment 4 Finding the change): the worker names nothing; bs finds its change from
// its branch: created since this spawn (or still open), never a fork's.
func TestContractFindTheChange(t *testing.T) {
	gcase(t, "find-the-change", "T3", func(t *testing.T, x *toy, b ghBackend) {
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		url := b.open(x, 1, sp.Workspace.Dir, "toy-1", false)
		must(t, x.worker("toy-1", 1, "done"), 0, "handed off to queen-toy", url)
	})
	gcase(t, "done-no-change", "T3", func(t *testing.T, x *toy, b ghBackend) {
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		x.push(sp.Workspace.Dir, "toy-1")
		must(t, x.worker("toy-1", 1, "done"), 7, "next: gh pr create")
	})
	gcase(t, "name-another-workers-pr", "T3", func(t *testing.T, x *toy, b ghBackend) {
		x.spawn("toy-1", "pr")
		sp2 := x.spawn("toy-2", "pr")
		b.commit(x, sp2.Workspace.Dir)
		theirs := b.open(x, 2, sp2.Workspace.Dir, "toy-2", false)
		must(t, x.worker("toy-1", 1, "done", "--pr", theirs), 2, "flag provided but not defined: -pr")
	})
	gcase(t, "fork-pr-ignored", "T3", func(t *testing.T, x *toy, b ghBackend) {
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		x.push(sp.Workspace.Dir, "toy-1")
		fork := x.openPR(1, "toy-1", func(pr *fake.PR) { pr.Fork, pr.Created = "someone/toy", time.Now() })
		r := x.worker("toy-1", 1, "done")
		must(t, r, 7, "next: gh pr create")
		lacks(t, r, fork)
	})
	gcase(t, "old-merged-pr-ignored", "T3", func(t *testing.T, x *toy, b ghBackend) {
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		x.push(sp.Workspace.Dir, "toy-1")
		old := x.openPR(1, "toy-1", func(pr *fake.PR) {
			pr.State, pr.Created = "MERGED", sp.TS.Add(-time.Hour)
			pr.HeadOID = x.git(sp.Workspace.Dir, "rev-parse", "HEAD")
		})
		r := x.worker("toy-1", 1, "done")
		must(t, r, 7, "next: gh pr create")
		lacks(t, r, old)
		must(t, x.queen("end", "toy-1"), 9)
	})
	gcase(t, "two-open-refused", "T3", func(t *testing.T, x *toy, b ghBackend) {
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		one := b.open(x, 1, sp.Workspace.Dir, "toy-1", false)
		two := x.openPR(2, "toy-1", func(pr *fake.PR) { pr.Created = time.Now() })
		must(t, x.worker("toy-1", 1, "done"), 7, one, two)
	})
	gcase(t, "draft", "T3", func(t *testing.T, x *toy, b ghBackend) {
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		b.open(x, 1, sp.Workspace.Dir, "toy-1", true)
		must(t, x.worker("toy-1", 1, "done"), 7, "pr_not_draft")
	})
	gcase(t, "not-pushed", "T3", func(t *testing.T, x *toy, b ghBackend) {
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		b.open(x, 1, sp.Workspace.Dir, "toy-1", false)
		b.commit(x, sp.Workspace.Dir)
		must(t, x.worker("toy-1", 1, "done"), 7, "push it")
	})
	gcase(t, "host-lag", "T3", func(t *testing.T, x *toy, b ghBackend) {
		sp := x.spawn("toy-1", "pr")
		dir := sp.Workspace.Dir
		b.commit(x, dir)
		url := b.open(x, 1, dir, "toy-1", false)
		b.refresh(x, url)
		x.gh.Edit(url, func(pr *fake.PR) { pr.Stale = true })
		b.commit(x, dir)
		x.git(dir, "push", "-q", "-u", "origin", "toy-1")
		must(t, x.worker("toy-1", 1, "done"), 10, "pending")
	})
	gcase(t, "host-ahead", "T3", func(t *testing.T, x *toy, b ghBackend) {
		sp := x.spawn("toy-1", "pr")
		dir := sp.Workspace.Dir
		b.commit(x, dir)
		b.open(x, 1, dir, "toy-1", false)
		other := filepath.Join(filepath.Dir(x.repo), "helper")
		x.git(x.repo, "fetch", "-q", "origin")
		x.git(x.repo, "worktree", "add", "-q", "--detach", other, "origin/toy-1")
		b.commit(x, other)
		x.push(other, "toy-1")
		must(t, x.worker("toy-1", 1, "done"), 7, "pull them, then push")
	})
	gcase(t, "end-before-merge", "T3", func(t *testing.T, x *toy, b ghBackend) {
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		url := b.open(x, 1, sp.Workspace.Dir, "toy-1", false)
		must(t, x.worker("toy-1", 1, "done"), 0)
		must(t, x.queen("end", "toy-1"), 9, url+": open")
		b.land(x, 1)
		must(t, x.queen("end", "toy-1"), 0)
	})
	// Q11: a done recorded before Amendment 4 named its PRs; bs end reads them for one
	// release, so the spawn ends as it was handed off.
	gcase(t, "done-prs-migration", "", func(t *testing.T, x *toy, b ghBackend) {
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		url := b.open(x, 1, sp.Workspace.Dir, "toy-1", false)
		x.rawEntry("done", "toy-1", "queen-toy", sp.ID, `{"bead":"toy-1","prs":["`+url+`"],"checks":[{"name":"no_background","pass":true}]}`)
		must(t, x.queen("end", "toy-1"), 9, url+": open")
		b.land(x, 1)
		must(t, x.queen("end", "toy-1"), 0, "ended toy-1")
	})
}

// refresh makes the fake GitHub read url's head now, as a view would.
func (ghBackend) refresh(x *toy, url string) {
	x.t.Helper()
	if _, err := x.gh.Run(context.Background(), "", "gh", "pr", "view", url, "--json", "headRefOid"); err != nil {
		x.t.Fatal(err)
	}
}

// T4 (Amendment 4: one --goal per spawn): a mixed goal recorded before it still decodes,
// shows in status and prunes on its union, but can't be amended or handed off.
func TestContractOldMixedEntry(t *testing.T) {
	k := newCard(t)
	sp := k.x.spawn("toy-1", "verdict")
	k.x.spawn("toy-9", "note")
	verdict := k.x.sqlOne(`SELECT json_extract(data, '$.goal.parts[0]') FROM entries WHERE kind = 'spawned' AND subject = 'toy-1'`)
	note := k.x.sqlOne(`SELECT json_extract(data, '$.goal.parts[0]') FROM entries WHERE kind = 'spawned' AND subject = 'toy-9'`)
	mixed := `{"by":"keeper","goal":{"parts":[` + verdict + `,` + note + `]}}`
	k.x.rawEntry("goal", "keeper", "toy-1", sp.ID, mixed)
	sp2 := k.x.spawn("toy-2", "verdict") // the amend case's own, so a failed refusal leaves toy-1 mixed
	k.x.rawEntry("goal", "keeper", "toy-2", sp2.ID, mixed)
	k.c("mixed-status", "", func(t *testing.T, x *toy) { must(t, x.keeper("status"), 0, "verdict note") })
	k.c("mixed-refuses-amend", "T4", func(t *testing.T, x *toy) {
		must(t, x.queen("goal", "toy-2", "verdict"), 2, "one goal per spawn", "discard")
	})
	k.c("mixed-refuses-done", "T4", func(t *testing.T, x *toy) {
		x.beads.Add(port.Bead{ID: "toy-1", Title: "work on toy-1", Description: "do work on toy-1", Notes: "n"})
		must(t, k.w(x, "toy-1", "done", "--answer", "yes", "--report", "ok"), 2, "spawned before one goal per spawn: end it")
	})
	k.c("mixed-ends-and-prunes", "", func(t *testing.T, x *toy) {
		must(t, x.queen("end", "--incomplete", "toy-1"), 0, "ended toy-1")
		must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir)
	})
}

// T7 (#118's fixes, bees-13.53): a pr spawn ended --incomplete whose PR closed unmerged
// can never complete: status's stuck: line names the keeper's discard, and runs as printed.
func TestContractStuckOnAClosedPR(t *testing.T) {
	gcase(t, "stuck-closed-pr", "T7", func(t *testing.T, x *toy, b ghBackend) {
		sp := x.spawn("toy-1", "pr")
		b.commit(x, sp.Workspace.Dir)
		b.open(x, 1, sp.Workspace.Dir, "toy-1", false)
		must(t, x.worker("toy-1", 1, "done"), 0)
		b.close(x, 1)
		must(t, x.queen("end", "--incomplete", "toy-1"), 0)
		must(t, x.keeper("status"), 0, "stuck: toy-1:", "closed", "bs goal toy-1 discard")
	})
}

// target opens PR n from a branch nobody spawned (the keeper's, an outside contributor's).
func (x *toy) target(b ghBackend, n int, branch string) (dir, url string) {
	x.t.Helper()
	dir = filepath.Join(filepath.Dir(x.repo), branch)
	x.git(x.repo, "worktree", "add", "-q", "-b", branch, dir)
	b.commit(x, dir)
	return dir, b.open(x, n, dir, branch, false)
}

// T5 (Amendment 4: pr=<url> stacks; push=<url> is narrow; no detached workspace).
func TestContractStackedAndPush(t *testing.T) {
	for _, c := range []struct{ id, state, fork, why string }{{"pr-target-merged", "MERGED", "", "merged"}, {"pr-target-closed", "CLOSED", "", "closed"}, {"pr-target-fork", "OPEN", "someone/toy", "fork"}} {
		gcase(t, c.id, "T5", func(t *testing.T, x *toy, b ghBackend) {
			_, url := x.target(b, 5, "feature")
			x.gh.Edit(url, func(pr *fake.PR) { pr.State, pr.Fork = c.state, c.fork })
			x.bead("toy-1", "stacked")
			r := x.queen("spawn", "--goal", "pr="+url, "toy-1")
			must(t, r, 2, c.why)
			lacks(t, r, "takes no target")
		})
	}
	gcase(t, "stacked-cut", "T5", func(t *testing.T, x *toy, b ghBackend) {
		tdir, url := x.target(b, 5, "feature")
		sp := x.spawn("toy-1", "pr="+url)
		if sp.Workspace.Branch != "toy-1" || sp.Workspace.Base != "feature" || x.git(sp.Workspace.Dir, "rev-parse", "HEAD") != x.git(tdir, "rev-parse", "HEAD") {
			fatalf(t, "stacked workspace %+v at %s: want branch toy-1 cut at feature's head, base feature", sp.Workspace, x.git(sp.Workspace.Dir, "rev-parse", "HEAD"))
		}
	})
	gcase(t, "stacked-handoff-after-target-merged", "T5", func(t *testing.T, x *toy, b ghBackend) {
		_, url := x.target(b, 5, "feature")
		sp := x.spawn("toy-1", "pr="+url)
		b.commit(x, sp.Workspace.Dir)
		b.open(x, 6, sp.Workspace.Dir, "toy-1", false)
		x.gh.Edit(b.url(6), func(pr *fake.PR) { pr.Base = "feature" })
		b.land(x, 5)
		must(t, x.worker("toy-1", 1, "done"), 7, "gh pr edit 6 --base main")
	})
	gcase(t, "stacked-merged-into-live-target", "T5", func(t *testing.T, x *toy, b ghBackend) {
		_, url := x.target(b, 5, "feature")
		sp := x.spawn("toy-1", "pr="+url)
		b.commit(x, sp.Workspace.Dir)
		mine := b.open(x, 6, sp.Workspace.Dir, "toy-1", false)
		x.gh.Edit(mine, func(pr *fake.PR) { pr.Base = "feature" })
		must(t, x.worker("toy-1", 1, "done"), 0)
		x.gh.Edit(mine, func(pr *fake.PR) { pr.State = "MERGED" }) // into feature, which is still open
		must(t, x.queen("end", "toy-1"), 9, "merged into feature")
	})
	gcase(t, "stacked-lower-end-refused", "T5", func(t *testing.T, x *toy, b ghBackend) {
		lower := x.spawn("toy-1", "pr")
		b.commit(x, lower.Workspace.Dir)
		url := b.open(x, 1, lower.Workspace.Dir, "toy-1", false)
		must(t, x.worker("toy-1", 1, "done"), 0)
		x.spawn("toy-2", "pr="+url)
		b.land(x, 1)
		must(t, x.queen("end", "toy-1"), 2, "gh pr edit")
		must(t, x.queen("prune"), 2, "gh pr edit")
	})
	gcase(t, "stacked-prune-falls-back-to-trunk", "T5", func(t *testing.T, x *toy, b ghBackend) {
		_, url := x.target(b, 5, "feature")
		sp := x.spawn("toy-1", "pr="+url)
		b.commit(x, sp.Workspace.Dir)
		b.open(x, 6, sp.Workspace.Dir, "toy-1", false)
		must(t, x.worker("toy-1", 1, "done"), 0)
		b.land(x, 5)
		x.git(x.repo, "worktree", "remove", "--force", filepath.Join(filepath.Dir(x.repo), "feature"))
		x.git(x.repo, "branch", "-D", "feature")
		b.land(x, 6) // retargeted onto main, then merged
		must(t, x.queen("end", "toy-1"), 0)
		must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir)
	})
	gcase(t, "push-owned-refused", "T5", func(t *testing.T, x *toy, b ghBackend) {
		owner := x.spawn("toy-1", "pr")
		b.commit(x, owner.Workspace.Dir)
		url := b.open(x, 1, owner.Workspace.Dir, "toy-1", false)
		x.bead("toy-2", "fixup")
		must(t, x.queen("spawn", "--goal", "push="+url, "toy-2"), 2, "next: bs spawn toy-1")
	})
	gcase(t, "push-own-branch", "T5", func(t *testing.T, x *toy, b ghBackend) {
		tdir, url := x.target(b, 5, "feature")
		sp := x.spawn("toy-1", "push="+url)
		dir := sp.Workspace.Dir
		if sp.Workspace.Branch != "toy-1" || x.git(dir, "rev-parse", "HEAD") != x.git(tdir, "rev-parse", "HEAD") {
			fatalf(t, "push workspace %+v: want its own branch toy-1 at feature's head", sp.Workspace)
		}
		if up := x.git(dir, "rev-parse", "--abbrev-ref", "toy-1@{upstream}"); up != "origin/feature" {
			fatalf(t, "toy-1's upstream is %q, want origin/feature", up)
		}
	})
	gcase(t, "push-handoff", "", func(t *testing.T, x *toy, b ghBackend) {
		_, url := x.target(b, 5, "feature")
		sp := x.spawn("toy-1", "push="+url)
		must(t, x.worker("toy-1", 1, "done"), 7, "commit")
		b.commit(x, sp.Workspace.Dir)
		must(t, x.worker("toy-1", 2, "done"), 7, "git push origin HEAD:feature")
		x.push(sp.Workspace.Dir, "feature")
		must(t, x.worker("toy-1", 3, "done"), 0, "handed off")
	})
	gcase(t, "push-target-closed", "", func(t *testing.T, x *toy, b ghBackend) {
		_, url := x.target(b, 5, "feature")
		sp := x.spawn("toy-1", "push="+url)
		b.commit(x, sp.Workspace.Dir)
		x.push(sp.Workspace.Dir, "feature")
		b.close(x, 5)
		must(t, x.worker("toy-1", 1, "done"), 7, "closed")
	})
	// bees-13.55: a discarded push= workspace is pruned, never kept for a detached HEAD.
	gcase(t, "push-discarded-pruned", "T5", func(t *testing.T, x *toy, b ghBackend) {
		_, url := x.target(b, 5, "feature")
		sp := x.spawn("toy-1", "push="+url)
		b.commit(x, sp.Workspace.Dir)
		must(t, x.keeper("goal", "toy-1", "discard"), 0)
		must(t, x.queen("end", "toy-1"), 0)
		must(t, x.queen("prune"), 0, "removed "+sp.Workspace.Dir)
	})
}

// T6 (Amendment 4 review=<url>): bs posts the worker's report as one --comment review
// carrying bs:<spawn id>, records its URL, and never approves or requests changes: the
// workers run as the keeper's account, and GitHub refuses both on its own PR.
func TestContractReviewPostedByBs(t *testing.T) {
	reviews := func(x *toy, url string) []fake.Comment {
		var rs []fake.Comment
		x.gh.Edit(url, func(pr *fake.PR) { rs = slices.Clone(pr.Reviews) })
		return rs
	}
	gcase(t, "review-posts-once", "T6", func(t *testing.T, x *toy, b ghBackend) {
		_, url := x.target(b, 5, "feature")
		sp := x.spawn("toy-1", "review="+url)
		must(t, x.shellRun(x.repo, x.env, "looks right\n", "done", "--as", "toy-1", "--answer", "approve", "--report", "-"), 0)
		rs := reviews(x, url)
		if len(rs) != 1 || rs[0].State != "COMMENTED" || !strings.Contains(rs[0].Body, "looks right") || !strings.Contains(rs[0].Body, "bs:"+string(sp.ID)) {
			fatalf(t, "reviews on the target: %+v", rs)
		}
		if d := x.sqlOne(`SELECT data FROM entries WHERE kind = 'done' AND ref = ?`, string(sp.ID)); !strings.Contains(d, "#pullrequestreview-") {
			fatalf(t, "the done entry records no review URL: %s", d)
		}
		if _, ok, _ := x.open().LandedOf(context.Background(), sp.ID); !ok {
			fatalf(t, "review= didn't land at handoff")
		}
	})
	// A retry after a lost response: the review bs posted is there, so nothing more is.
	gcase(t, "review-retry-posts-nothing", "T6", func(t *testing.T, x *toy, b ghBackend) {
		_, url := x.target(b, 5, "feature")
		sp := x.spawn("toy-1", "review="+url)
		x.gh.Edit(url, func(pr *fake.PR) {
			pr.Reviews = append(pr.Reviews, fake.Comment{Author: "jmmshn", Body: "looks right\n\nbs:" + string(sp.ID), At: time.Now(), ID: 99, State: "COMMENTED"})
		})
		must(t, x.keeper("done", "--as", "toy-1", "--answer", "changes", "--report", "looks right"), 0)
		if rs := reviews(x, url); len(rs) != 1 {
			fatalf(t, "%d reviews after a retry, want 1: %+v", len(rs), rs)
		}
	})
	gcase(t, "review-answer-yes", "T6", func(t *testing.T, x *toy, b ghBackend) {
		_, url := x.target(b, 5, "feature")
		x.spawn("toy-1", "review="+url)
		must(t, x.keeper("done", "--as", "toy-1", "--answer", "yes", "--report", "r"), 7, "approve|changes")
	})
	gcase(t, "review-never-approves", "T6", func(t *testing.T, x *toy, b ghBackend) {
		_, url := x.target(b, 5, "feature")
		x.spawn("toy-1", "review="+url)
		must(t, x.keeper("done", "--as", "toy-1", "--answer", "approve", "--report", "r"), 0)
		posted := false
		for _, c := range x.gh.Calls {
			if len(c) > 2 && c[1] == "pr" && c[2] == "review" {
				posted = true
				if slices.Contains(c, "--approve") || slices.Contains(c, "--request-changes") || slices.Contains(c, "-a") || slices.Contains(c, "-r") {
					fatalf(t, "bs asked gh to approve or request changes: %q", c)
				}
			}
		}
		if !posted {
			fatalf(t, "bs posted no review")
		}
	})
	// The fake encodes GitHub's refusal (motif 13): approving one's own PR is refused.
	gcase(t, "fake-gh-refuses-own-approve", "", func(t *testing.T, x *toy, b ghBackend) {
		_, url := x.target(b, 5, "feature")
		if _, err := x.gh.Run(context.Background(), "", "gh", "pr", "review", url, "--approve"); err == nil || !strings.Contains(err.Error()+string(errStderr(err)), "Can not approve your own pull request") {
			fatalf(t, "gh pr review --approve on its own PR: %v", err)
		}
	})
}

func errStderr(err error) []byte {
	var ee *port.ExitError
	if errors.As(err, &ee) {
		return ee.Stderr
	}
	return nil
}
