# The contract card

Every case of the keeper's 2026-10-22 post-swap pilot, as a replay of the handoff, end, done, prune and identity contract (ADR-0010 is the spec). Run it on any bs change that touches spawn, done, end, goal, prune or identity; a regression shows as a FAIL.

```sh
go build -o bin/bs ./cmd/bs
.claude/skills/live-test/setup.sh --no-queen         # no queen: nothing reacts to the workers' mail
.claude/skills/live-test/contract.sh <run dir>       # ~5 min; prints a line per case
.claude/skills/live-test/teardown.sh <run dir>       # the only deleter, as always
```

`contract.sh` runs the **offline tier**: no network, no GitHub. It prints `PASS`, `FAIL`, `XFAIL` or `XPASS` per case with the expected and actual exit code and output, writes the same as a markdown table to `<run>/keeper/contract/table.md` (paste it into the PR body), keeps each case's output in `<run>/keeper/contract/out/<case>.txt`, and exits 1 on any FAIL. It runs once per toy.

**Expected-fail.** A case whose fix hasn't landed names its bead (`xfail=bees-13.N` in the script) and is `XFAIL` while it fails. Once the fix lands it prints `XPASS (bees-13.N landed? drop its xfail mark)`: drop the mark in `contract.sh` in the fix's PR, so the case guards it from then on. Neither fails the card.

## How it drives workers

As the pilot did: one idle worker per group (`bs spawn --goal <goal>` on a bead whose brief says "do nothing: say idle and stop"), on `haiku` with the build first on its PATH (`projects/<toy>.toml`: `worker.path`, `worker.claude_code.model`). The keeper's shell plays the worker and the queen: `bs done --as <worker>`, `bs check`, `bs goal --as queen-<toy>`, `bs end --as queen-<toy>`, with `HERDR_SESSION=<toy>` and the toy repo as cwd. Work in a workspace (commits, files) is made from that shell in the worktree.

Two groups need code in a worker's pane, as a descendant of its harness:

- the **gate**: `herdr agent prompt` asks the worker to run `sleep 900` with `run_in_background`;
- **identity**: the command goes into the pane as Claude's `! <cmd>` shell, because the auto-mode classifier refuses the same command asked for in a prompt ("Credential Exploration", seen live).

The first worker spawns alone and the rest together: concurrent first spawns race the logbook's creation (bees-13.59). Workers run the build whatever `pane_bs` in live.json says, since `worker.path` puts it first; report both anyway.

The card commits a `.gitignore` (`bin/`, `scratch/`) to the toy's main before spawning, so ignored files behave the same whatever the keeper's global excludes say. The amend group adds an `origin` remote naming a GitHub repo that is never contacted (bs parses remotes locally) and removes it after.

## Offline cases

Each row: the case id (as `contract.sh` names it), the setup, the command, and the expected exit code and output fragment.

### Spawn refusals (exit 2 each)

| case | command | expect |
|---|---|---|
| spawn-no-goal | `bs spawn <bead>` | 2, `goal is required` |
| spawn-pr-merged | `--goal pr:merged` | 2, `pr has no state "merged"` |
| spawn-review-bare | `--goal review` | 2, `review needs a target` |
| spawn-two-bad | `--goal verdict=x --goal nope` | 2, both reported at once |
| spawn-push-dash | `--goal push=-x` | 2, `a target can't start with -` |
| spawn-review-no-remote | `--goal review=<url>`, the toy has no GitHub remote | 2, `belongs to no github repo` |
| spawn-unknown-bead | `bs spawn --goal verdict <toy>-99` | 2, `no bead` |

### verdict (worker V)

| case | command | expect |
|---|---|---|
| verdict-no-answer | `bs done` | 7, `--answer yes\|no is missing` |
| verdict-maybe | `--answer maybe --report x` | 7 |
| verdict-blank-report | `--answer yes --report "   "` | 7, `--report … is missing` |
| verdict-no-report | `--answer yes` | 7 |
| verdict-pr | `--pr <url> --answer yes --report x` | 2, `takes no --pr` |
| verdict-end-early | `bs end` before any handoff | 9, `next: bs end --incomplete` |
| verdict-stdin | `echo … \| bs done --answer no --report -` | 0, the report read from stdin; lands |
| verdict-done-after-landed | a second `bs done` after landing | 2 — **xfail bees-13.54** (accepted today) |
| verdict-end | `bs end` | 0 |
| verdict-end-again | `bs end` again | 2, `already ended` |
| verdict-done-after-end | `bs done` | 2, `no open spawn` |
| end-closes-workspace | the spawn's herdr workspace after end | gone |
| end-stops-harness | the recorded harness pid after end | dead |

### note (worker N)

| case | setup | command | expect |
|---|---|---|---|
| note-unchanged | — | `bs done` | 7, `notes and design are as they were` |
| note-answer | — | `bs done --answer yes --report x` | 2, next names `bd update` |
| note-changed | `bd update --notes changed` | `bs check` | 0 |
| note-reset | notes back to the spawn-time text (none) | `bs check` | 7 |
| note-design | `bd update --design …` | `bs check` | 0 |

### beads (worker B)

| case | setup | command | expect |
|---|---|---|---|
| beads-none | — | `bs done` | 7, `no bead was created under` |
| beads-elsewhere | a child under another bead | `bs check` | 7 |
| beads-own | a child under its own bead | `bs check` | 0 |

### branch (worker BR)

| case | setup | command | expect |
|---|---|---|---|
| branch-no-commit | — | `bs done` | 7, `branch_ahead` |
| branch-untracked | a commit, then an untracked file | `bs check` | 7, `clean` names the file |
| branch-ignored-bin | an ignored `bin/bs` | `bs check` | 0 |
| branch-modified | a tracked file modified | `bs check` | 7, names it |
| branch-pass | clean again | `bs done` | 0 |
| branch-end-early | — | `bs end` | 9, `branch_merged` |

### mixed (worker M, `--goal verdict --goal note`)

| case | setup | command | expect |
|---|---|---|---|
| mixed-both-fail | — | `bs done` | 7, both parts at once |
| mixed-one-fail | — | `bs done --answer yes --report ok` | 7, `note_changed` only |
| mixed-pass | notes changed | the same | 0 |

### Background gate (worker G)

Setup: the worker runs `sleep 900` in the background.

| case | command | expect |
|---|---|---|
| gate-check | `bs check` | 7, `FAIL no_background` naming `sleep 900` |
| gate-done | `bs done --answer yes --report r` | 5, lists the pids |
| gate-end-incomplete | `bs end --incomplete` | 5 |
| gate-end-incomplete-next | its `next:` | `bs end --incomplete --force` — **xfail bees-13.52** (today `bs end --force`) |
| gate-force-next | that `next:` followed: `bs end --force` | 9 whose `next:` is `--incomplete --force`, not a loop back — **xfail bees-13.52** |
| gate-done-force | `bs done --force …` | 0 |
| gate-waived | the `done` entry | `"waived":true` and the process list |
| gate-end | `bs end` | 5, `next: bs end --force` |
| gate-end-force | `bs end --force` | 0 |
| gate-sleep-stopped | the `sleep 900` pid | dead |

### Amend, waive, discard (workers A, D; M landed)

| case | command | expect |
|---|---|---|
| amend-other-repo | `bs goal A review=<a cli/cli PR>` | 2, `a change of cli/cli` |
| amend-unknown | `bs goal A nope` | 2 |
| amend-waive-unrun | `bs goal A --waive pr_pushed` on verdict | 2, `runs no such check` |
| amend-pr-draft | `bs goal A pr:draft` | 0 |
| amend-mailed | A's mail | `changed your goal: it is now --goal pr:draft` |
| amend-waive | `bs goal A --waive pr_pushed` | 0 |
| amend-as-worker | `bs goal --as A A verdict` | 2 — **xfail bees-13.50** (accepted today) |
| amend-after-landed | `bs goal M note` | 2 |
| discard-as-queen | `bs goal --as queen-<toy> A discard` | 0 |
| discard-as-queen-by | its `goal` entry | `"by":"keeper"` — **xfail bees-13.50** (records the queen today) |
| discard-keeper | `bs goal D discard` from the keeper's shell | 0 |
| discard-keeper-by | its `goal` entry | `"by":"keeper"` |

### End and prune

Setup: N (landed) has an ignored `scratch/report.md`; B (landed) only an ignored `bin/bs`; BR (unlanded) an ignored `scratch/keep.md`; D (discarded) a commit on no other branch.

| case | command | expect |
|---|---|---|
| end-note, end-beads, end-discarded | `bs end` | 0 |
| end-incomplete | `bs end --incomplete BR` | 0 |
| end-incomplete-keeps | BR's worktree, branch, ignored file | all still there |
| prune-exit | `bs prune` | 3, `kept some workspaces` |
| prune-keeps-unmerged-branch | | keeps BR, `has work not on main` |
| prune-keeps-scratch | | keeps N, names `scratch/report.md` |
| prune-removes-bin-only | | removes B and its branch |
| prune-removes-discarded | | removes D and its branch |
| prune-removed-discarded-commit | D's branch | gone: a discard throws its committed work away |

### Re-spawn and resume

| case | command | expect |
|---|---|---|
| respawn-kept | `bs spawn --goal branch <BR's bead>` | 0, warns `on its kept worktree and branch` |
| respawn-commit-counts | `bs check BR` | 0, the old commit counts |
| resume-stuck | kill I's harness, `bs status` | `stuck: … resumes it` |
| resume-other-goal | `bs spawn --goal note <I's bead>` | 2, `next: bs goal` |
| resume | `bs spawn --goal verdict <I's bead>` | 0, `resumed` in the same pane |

### Identity (from worker I's pane)

| case | command | expect |
|---|---|---|
| ident-goal-self | `bs goal I note` | 2, `a worker never changes a goal` |
| ident-bs-project | `BS_PROJECT=other bs status` | 0, warns and ignores it |
| ident-end-sibling | `bs end S1` | 2 — **xfail bees-13.49** (reaches the completion gate today) |
| ident-end-incomplete-sibling | `bs end --incomplete S2` | 2 — **xfail bees-13.49** (ends the sibling today) |
| ident-prune | `bs prune` | 2 — **xfail bees-13.49** (prunes today) |

## GitHub tier (by hand, until it has a repo)

These need a GitHub remote with a required CI check, so they are not in `contract.sh` yet: where they run (a throwaway repo, or `jmmshn/bees` as the pilot did) is the keeper's call. Each PR it opens is titled `[pilot] do not merge`; the run closes them and deletes their branches at the end, and says so in its report.

Also offline-shaped but host-bound: a review or push target that is **merged** (spawn and amend, 2).

**pr**

| case | expect |
|---|---|
| `bs done` with no `--pr` | 2 |
| another worker's PR | 7 `pr_workspace_commit` |
| a merged PR | 7, every failure listed |
| not a URL | 2 |
| a PR that doesn't exist | 7 |
| a draft PR | 7 `pr_not_draft` |
| a commit not pushed | 7 `pr_pushed`, says push |
| CI pending | 10, `next: … --wait` |
| `bs handoff` | waits for required CI, then 0 |
| `--replaces` a PR never handed off | 2 |
| `bs end` before merge | 9, names the PR's state |
| PR closed, `bs end` | 9, `closed` |
| `bs status` for a closed-unmerged PR ended `--incomplete` | `stuck:` names the keeper discard — **xfail bees-13.53** |
| a cherry-picked foreign commit | 7 |
| its own commit rebased | 0 |

**pr:draft**: no CI check; prune keeps it while the PR is open, removes it once closed, leaves the remote branch and says so.

**push=\<PR\>**

| case | expect |
|---|---|
| the workspace | starts at the target's head |
| `bs done` with no commit | 7 |
| a commit not pushed | 7, prints the exact `git push` |
| pushed, CI pending | 10 |
| the PR owner's `bs check` | fails `pr_pushed`, "pull them, then push" |
| target closed after spawn | handoff fails `pr_open` — **xfail bees-13.51** |
| discarded, then `bs prune` | removes the workspace — **xfail bees-13.55** (detached HEAD keeps it today) |

**review=\<PR\>**

| case | expect |
|---|---|
| no comment | 7 |
| a comment with another spawn's marker | 7 |
| a review carrying this spawn's `bs:<spawn id>` | 0 |
