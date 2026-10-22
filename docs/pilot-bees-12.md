# Pilot: bees-12 on pilot/bees-12

The bees-12 work (ADR-0009: one core, a Harness port, workspace kinds, delivery fragments, logbook v1, `bs done`) was built on the integration branch `pilot/bees-12`, never on `main`, and piloted in throwaway projects before the keeper merges it (bees-12.11, 2026-10-21). This page is the pilot's report and the case for the merge.

## Verdict

**Go**, with the rollout below run in order. Its step 2 ends every session the old `bs` started before the new one is installed: those sessions carry `BS_PROJECT` pins, which the new `bs` warns about but still obeys outside a recorded pane.

Every lifecycle the pilot exercised passed live: spawn, message, `bs done`, the background-work gate, end, prune, cross-project mail, `git_worktree` and `none` workspaces, `github_pr`, `manual` and fragment delivery, and setup from `SETUP-AGENT.md` by a fresh agent. Every P1 the pilot found was fixed and re-verified. The bakeoff replay passed all 20 cards, matching the October baseline on every card both played. It costs more keeper turns and context per run, and turned up two P2 bugs that recover by hand (bees-12.49, 12.50). Neither blocks the common path; fix them soon after the merge.

## How it ran

Eleven rounds, one per stage that merged into the pilot. Each ran in throwaway projects made by the `live-test` skill: a real queen and real Claude workers in herdr, driven by scripted keeper lines, then torn down with a leak check. The queen ran `./bin/bs` built from `origin/pilot/bees-12`; workers ran the shared `bs` (main) in rounds 1–2 and the pilot build from round 3 on (`bs spawn --path`, then `worker.path`). Transcripts are under `~/.cache/bees-live/<toy>/keeper/transcripts`.

| Round | Pilot tip | What ran | Result |
|---|---|---|---|
| 1 | ce67c94 | baseline (claude_code, git_worktree, github_pr) | pass; live guard attempt blocked by auto mode (later dropped) |
| 2 | 35242b8 | manual delivery, a fragment file, `allow_merge_into` without protection, static guards | pass; 4 beads |
| 3 | 90c6a3e | baseline with `bs spawn --path` | pass; queen omits `--path` unless told (12.24) |
| 4 | 1e74321 → f741153 | baseline via the new setup, manual, fragment, twin queen, agy refusals | pass; pilot red after a semantic merge (12.25, fixed) |
| 5 | 6760861 | labels and nesting, workspace `none` in git and non-git | labels pass; `none` in a git checkout failed (12.28, P1, fixed) |
| 6 | 43e1132 | `none` on `paths.repo`, `none` on a non-git dir | pass; spawn fan-out flake (12.33, fixed) |
| 7 | 110e472 | status without herdr, no env pins, killed worker, moved pane, cross-project mail | 5 pass; `bs end` killed a moved worker (12.36, P1, fixed), dead worker hidden (12.37), reply hint misrouted (12.38) |
| 8 | 6068ee5 | `bs done --pr/--report`, the gate on done and end | pass |
| 9 | 84dca3c | regression sweep of rounds 1–8 | 12/12 pass |
| 10 | c8100df | new-user path: three fresh agents on `SETUP-AGENT.md`; `bs config init`; `bs doctor` on a broken config; harness switch with a live worker | pass; doc gaps (12.42, fixed) |
| 11 | 69d88b3, smoke on 347fc83 | final sweep plus one fresh setup agent; #94 and #95 checked | pass except `bs end --force` on a moved worker (12.46) |
| 12 | 0a0fe7b | #97 and #98 live; bakeoff S1 and S18 | pass: `bs end --force` closes a moved worker's pane and its process; repo-free commands warn about a stray `BS_PROJECT` |

### Final sweep (round 11)

| Check | Result |
|---|---|
| Baseline: spawn, worker ↔ worker mail, done, end, prune | pass |
| `none` + `manual` on a non-git directory, no `BS_PROJECT` needed | pass |
| A delivery fragment file (a patch-based CL flow) | pass |
| Guards, static (deny globs per delivery kind) | pass (12.21 open) |
| Workers run the build under test (`worker.path`) | pass |
| herdr labels `<project> · <label>`, nested for `git_worktree` | pass |
| A moved pane stays running and is rung | pass |
| `bs end` refuses a workspace holding another worker's pane | pass |
| A killed worker shows `not running` | pass |
| Cross-project send and reply; a misaddressed send is refused | pass |
| `bs done` and the background-work gate (exit 5, `--force`) | pass |
| `bs prune` says why it keeps a leftover worktree | pass |
| `bs end --force` on a worker whose pane moved | fail (12.46); pass on 0a0fe7b after #97 |
| A fresh agent sets bees up from `SETUP-AGENT.md` alone | pass |
| `bs doctor`: older herdr server, `github_pr` without a remote, a stray `BS_PROJECT` | pass |
| Teardown of every toy | no leaks |

## Beads

Fixed in the pilot and re-verified live: 12.19, 12.20, 12.23, 12.24, 12.25, 12.27, 12.28, 12.29, 12.30, 12.31, 12.32, 12.33, 12.34, 12.35, 12.36, 12.37, 12.38, 12.40, 12.41, 12.42, 12.43, 12.44, 12.45, 12.46, 12.47, 12.48.

Open:

| Bead | P | What | Why still open |
|---|---|---|---|
| 12.49 | 2 | `bs spawn` reports failure while the worker started, and records no start; `bs done`/`bs end` then need workarounds | bakeoff, 2 of 20 runs on 347fc83; not seen in 3 spawns on 0a0fe7b |
| 12.50 | 2 | the background-work gate counts `bs done … \| tail` and a worker's own reply-wait loop | bakeoff; `--force` works around it |
| 12.51 | 3 | +12 keeper turns from post-merge cleanup; workers try a positional `bs done "<text>"`; who merges drifts | bakeoff; prompt wording |
| 12.21 | 3 | guard globs deny branches like `maintenance`; `manual` allows force-push | permission code: the keeper okays the edit |
| 12.39 | 3 | auto mode denies the queen's `bs bead new`/`spawn` when a brief mentions a server | permission behaviour: needs the keeper |
| 12.22 | 3 | toy and bakeoff sessions load the keeper's global `CLAUDE.md` | deferred by the keeper |
| 12.26 | 2 | toy workers read the real bees config, not the toy's | deferred by the keeper |

## Needs the keeper

1. **agy.** The adapter is staged; every refusal names bees-12.12's probes. No live agy run happened.
2. **12.21 and 12.39**: edits to permission code, okayed in the worker's pane.

Out of the pilot, by the keeper's call (2026-10-21):

- **The live guard attempt** (a toy worker trying `git push origin HEAD:main` and `gh pr merge`) is dropped; the static check of the deny globs stands.
- **hg** is dropped: `none` on an hg checkout is untested, as hg isn't installed on the pilot machine.
- **12.22 and 12.26** are deferred: toy and bakeoff sessions still load the keeper's global `CLAUDE.md` and real bees config, as every bakeoff round so far has.
- oh_my_pi has no adapter yet.

## Rollout

1. The keeper merges `pilot/bees-12` into `main`.
2. In every project, `bs end` every worker and exit every queen the old `bs` started, and unset any `BS_PROJECT`, `BS_LOGBOOK` or `BS_LOGBOOK_DIR` in the keeper's shells. Old sessions have no start entry, so the new `bs` can't ring them; it lists them as not running.
3. In the main checkout: `git pull` on `main`, `go install ./cmd/bs`; `bs version` prints the merge commit.
4. The first `bs` call on each logbook migrates it to v1, copying the old file to `<project>.db.pre-1` first. Check `~/.local/share/bees/<project>.db.pre-1` exists for each project, and keep the copies until the fleet has run for a day.
5. Optionally `bs config init` (machine-wide file), then `bs doctor` in each project's repo.
6. Restart each queen (`bs queen` in its pane) and respawn workers (`bs spawn <bead>`). Only sessions started on the new `bs` can be rung.

## Bakeoff replay

The role prompts, spawn and messaging all changed, so every card was replayed on arm C (bees) against the pilot build, by keeper agents following [`bakeoff/cards/DRIVER.md`](../bakeoff/cards/DRIVER.md). That was 20 cards on 347fc83, then S1 and S18 again on 0a0fe7b after #97 and #98. Arms A and B run no `bs`, so their October rows stand as the baseline. Runs use `BAKEOFF_TAG` ids (`t1cp`) so no older round's logbook is reused. Raw runs are in `~/.cache/bees-bakeoff/runs-pilot`; the rows are in [`bakeoff/HISTORY.md`](../bakeoff/HISTORY.md).

On the 19 cards both rounds played:

| | A subagents | B peers | C, Oct 16 | C, pilot |
|---|---|---|---|---|
| Checks passed | 18/19 | 18/19 | 19/19 | **19/19** |
| Keeper turns (sum) | 70 | 59 | 59 | 71 |
| Runs leaving something behind | 10 | 10 | 14 | 11 |
| Context tokens per run (mean) | 1.02 M | 1.02 M | 1.16 M | 1.35 M |

- **What held.** Every card passed, including the durability cards bees exists for. A fresh queen found its running worker from `bs status` (S10). A killed worker resumed in its own conversation (S11). Mail to a downed worker was read when it came back (S12). The keeper's own worktree was left alone (S17). Ending by bead id, then respawning with the same label, worked (S18).
- **What cost more.** Twelve more keeper turns, mostly a separate `clean up` after `merge it`: the queen leaves the local branch for `bs prune` and the origin branch for later, and deleting it is gated by auto mode unevenly. Every leftover is an origin branch (bees-1.34). Context per run rose about 16%, in line with the larger role prompts.
- **What broke.** Two spawns reported failure though their workers had started (bees-12.49). The gate refused `bs done` for its own pipeline's `tail` and for a worker's reply-wait loop (bees-12.50). Workers try `bs done "<text>"` before `--report` (bees-12.51).
