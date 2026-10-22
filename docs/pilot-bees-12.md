# Pilot: bees-12 on pilot/bees-12

The bees-12 work (ADR-0009: one core, a Harness port, workspace kinds, delivery fragments, logbook v1, `bs done`) was built on the integration branch `pilot/bees-12`, never on `main`, and piloted in throwaway projects before the keeper merges it (bees-12.11, 2026-10-21). This page is the pilot's report and the case for the merge.

## Verdict

**Go**, with the rollout below run in order. Its step 2 ends every session the old `bs` started before the new one is installed: those sessions carry `BS_PROJECT` pins, which the new `bs` warns about but still obeys outside a recorded pane.

Every lifecycle the pilot exercised passed live: spawn, message, `bs done`, the background-work gate, end, prune, cross-project mail, `git_worktree` and `none` workspaces, `github_pr`, `manual` and fragment delivery, and setup from `SETUP-AGENT.md` by a fresh agent. Every P1 the pilot found was fixed and re-verified. The bakeoff replay passed all 20 cards, matching bees' own October results on every card both rounds played. It costs more keeper turns and context per run, and turned up two new P2 bugs that recover by hand: bees-12.49 (spawn reports failure though the worker started) and bees-12.50 (the gate counts `bs done`'s own pipeline). Neither blocks the common path; fix them soon after the merge.

## How it ran

Twelve rounds, one per stage that merged into the pilot. Each ran in throwaway projects (toys) made by the `live-test` skill: a real queen and real Claude workers in herdr, driven by scripted keeper lines, then torn down with a leak check. The queen ran `./bin/bs` built from `origin/pilot/bees-12`; workers ran the shared `bs` (main) in rounds 1–2 and the pilot build from round 3 on (`bs spawn --path`, then `worker.path`). Each toy's transcripts are in `~/.cache/bees-live/<toy>/keeper/transcripts`, and a setup agent's report in `keeper/report.md` beside them.

| Round | Pilot tip | What ran | Result | Toys (`lt26102…`) |
|---|---|---|---|---|
| 1 | ce67c94 | baseline (claude_code, git_worktree, github_pr) | pass; live guard attempt blocked by auto mode (later dropped) | 12026198184 |
| 2 | 35242b8 | manual delivery, a fragment file, `allow_merge_into` without protection, static guards | pass; 4 beads | 12028555096 |
| 3 | 90c6a3e | baseline with `bs spawn --path` | pass; queen omits `--path` unless told (bees-12.24) | 12036542825 |
| 4 | 1e74321 → f741153 | baseline via the new setup, manual, fragment, twin queen, agy refusals | pass; pilot red after a semantic merge (bees-12.25, fixed) | 12048089756, 12048081959, 12048082238, 12051034418, 12051556802 |
| 5 | 6760861 | labels and nesting, workspace `none` in git and non-git | labels pass; `none` in a git checkout failed (bees-12.28, P1, fixed) | 12053544360, 12053541892, 12055498315 |
| 6 | 43e1132 | `none` on `paths.repo`, `none` on a non-git dir | pass; spawn fan-out flake (bees-12.33, fixed) | 12119063335, 12119064557 |
| 7 | 110e472 | status without herdr, no env pins, killed worker, moved pane, cross-project mail | 5 pass; `bs end` killed a moved worker (bees-12.36, P1, fixed), dead worker hidden (bees-12.37), reply hint misrouted (bees-12.38) | 12249520393, 12249529680 |
| 8 | 6068ee5 | `bs done --pr/--report`, the gate on done and end | pass | 12305286475, 12305284171 |
| 9 | 84dca3c | regression sweep of rounds 1–8 | 12/12 pass | 12314559008, 12314550811, 12314559200, 12314555153 |
| 10 | c8100df | new-user path: three fresh agents on `SETUP-AGENT.md`; `bs config init`; `bs doctor` on a broken config; harness switch with a live worker | pass; doc gaps (bees-12.42, fixed) | 12330239622, 12330232708, 12330237019 |
| 11 | 69d88b3, smoke on 347fc83 | final sweep plus one fresh setup agent; #94 and #95 checked | pass except `bs end --force` on a moved worker (bees-12.46) | 12350191488, 12350199006, 12350199187, 12350199445, 12350197705, 12356435571 |
| 12 | 0a0fe7b | #97 and #98 live; bakeoff S1 and S18 | pass: `bs end --force` closes a moved worker's pane and its process; repo-free commands warn about a stray `BS_PROJECT` | 20020389217 |

### Final sweep (round 11)

| Check | Result |
|---|---|
| Baseline: spawn, worker ↔ worker mail, done, end, prune | pass |
| `none` + `manual` on a non-git directory, no `BS_PROJECT` needed | pass |
| A delivery fragment file (a patch-based CL flow) | pass |
| Guards, static (deny globs per delivery kind) | pass (bees-12.21 open) |
| Workers run the build under test (`worker.path`) | pass |
| herdr labels `<project> · <label>`, nested for `git_worktree` | pass |
| A moved pane stays running and is rung | pass |
| `bs end` refuses a workspace holding another worker's pane | pass |
| A killed worker shows `not running` | pass |
| Cross-project send and reply; a misaddressed send is refused | pass |
| `bs done` and the background-work gate (exit 5, `--force`) | pass |
| `bs prune` says why it keeps a leftover worktree | pass |
| `bs end --force` on a worker whose pane moved | fail (bees-12.46); pass on 0a0fe7b after #97 |
| A fresh agent sets bees up from `SETUP-AGENT.md` alone | pass |
| `bs doctor`: older herdr server, `github_pr` without a remote, a stray `BS_PROJECT` | pass |
| Teardown of every toy | no leaks |

## Beads

Fixed in the pilot, by PR: bees-12.19, 12.20 and 12.24 (#72), 12.23 (#73), 12.25 (#75), 12.27 (#76), 12.30 (#77), 12.28, 12.29 and 12.31 (#78), 12.32 (#80), 12.34 and 12.35 (#82), 12.33 (#83), 12.36 (#84), 12.37 (#86), 12.38 (#87), 12.40 (#88), 12.41 (#91), 12.42 (#92), 12.43 (#93), 12.44 (#94), 12.45 (#95), 12.48 (#96), 12.46 (#97), 12.47 (#98). Each behaviour fix was rechecked in a later round or the final sweep.

Open:

| Bead | P | What | Why still open |
|---|---|---|---|
| bees-12.49 | 2 | `bs spawn` reports failure while the worker started, and records no start; `bs done` and `bs end` then need workarounds | bakeoff, 2 of 20 runs on 347fc83; not seen in 3 spawns on 0a0fe7b |
| bees-12.50 | 2 | the background-work gate counts `bs done … \| tail` and a worker's own reply-wait loop | bakeoff; `--force` works around it |
| bees-12.21 | 3 | guard globs deny branches like `maintenance`; `manual` allows force-push | permission code: the keeper okays the edit |
| bees-12.51 | 3 | bakeoff friction in the prompts: a separate `clean up` turn after `merge it`, positional `bs done "<text>"`, who merges | prompt wording |
| bees-12.22 | 3 | toy and bakeoff sessions load the keeper's global `CLAUDE.md` | deferred by the keeper |
| bees-12.26 | 3 | toy workers read the real bees config, not the toy's | deferred by the keeper |

## Needs the keeper

1. **agy.** The adapter is staged; every refusal names bees-12.12's probes. No live agy run happened.
2. **bees-12.21 (guard globs)**: an edit to permission code, okayed in the worker's pane.

Out of the pilot, by the keeper's call (2026-10-21):

- **The live guard attempt** (a toy worker trying `git push origin HEAD:main` and `gh pr merge`) is dropped: the static check of the deny globs, the unit tests and GitHub's branch protection stand.
- **bees-12.39 (auto mode denies a brief that mentions a server)** is dropped.
- **hg** is dropped: `none` on an hg checkout is untested, as hg isn't installed on the pilot machine.
- **bees-12.22 and bees-12.26 (toy sessions see the keeper's own instructions and config)** are deferred at P3: toy and bakeoff sessions still load the keeper's global `CLAUDE.md` and real bees config, as every bakeoff round so far has.
- oh_my_pi has no adapter yet.

## Rollout

1. The keeper merges `pilot/bees-12` into `main`.
2. In every project, `bs end` every worker and exit every queen the old `bs` started, and unset any `BS_PROJECT`, `BS_LOGBOOK` or `BS_LOGBOOK_DIR` in the keeper's shells. Old sessions have no start entry, so the new `bs` can't ring them; it lists them as not running.
3. In the main checkout: `git pull` on `main`, `go install ./cmd/bs`; `bs version` prints the merge commit.
4. The first `bs` call on each logbook migrates it to v1, copying the old file to `<project>.db.pre-1` first. Check `~/.local/share/bees/<project>.db.pre-1` exists for each project, and keep the copies until the fleet has run for a day.
5. Optionally `bs config init` (machine-wide file), then `bs doctor` in each project's repo.
6. Restart each queen (`bs queen` in its pane) and respawn workers (`bs spawn <bead>`). Only sessions started on the new `bs` can be rung.

## Bakeoff replay

The role prompts, spawn and messaging all changed, so every card was replayed on arm C (bees) against the pilot build, by keeper agents following [`bakeoff/cards/DRIVER.md`](../bakeoff/cards/DRIVER.md). That was 20 cards on 347fc83 (all but S3, S4 and S5, which their b and c cards replace), then S1 and S18 again on 0a0fe7b after #97 and #98. Arms A and B run no `bs`, so their October rows stand as the baseline. Runs use `BAKEOFF_TAG` ids (`t1cp`) so no older round's logbook is reused. Raw runs are in `~/.cache/bees-bakeoff/runs-pilot`; the rows are in [`bakeoff/HISTORY.md`](../bakeoff/HISTORY.md).

On the 19 cards both rounds played (S18 is new since October; arm A's S14 is its rerun t14ra; October's figures from [`bakeoff/results/table.md`](../bakeoff/results/table.md)):

| | A subagents | B peers | C, Oct 16 | C, pilot |
|---|---|---|---|---|
| Checks passed | 18/19 | 18/19 | 19/19 | **19/19** |
| Keeper turns (sum) | 69 | 56 | 59 | 71 |
| Runs leaving something behind | 10 | 10 | 14 | 11 |
| Context tokens per run (mean) | 1.02 M | 1.02 M | 1.16 M | 1.35 M |

- **What held.** Every card passed, including the durability cards bees exists for. A fresh queen found its running worker from `bs status` (S10). A killed worker resumed in its own conversation (S11). Mail to a downed worker was read when it came back (S12). The keeper's own worktree was left alone (S17). Ending by bead id, then respawning with the same label, worked (S18).
- **What cost more.** Twelve more keeper turns, mostly a separate `clean up` after `merge it`: the queen leaves the local branch for `bs prune` and the origin branch for later, and deleting it is gated by auto mode unevenly. Every leftover is an origin branch (bees-1.34). Context per run rose about 16%, in line with the larger role prompts.
- **What broke.** Two spawns reported failure though their workers had started (bees-12.49). The gate refused `bs done` for its own pipeline's `tail` and for a worker's reply-wait loop (bees-12.50). Workers try `bs done "<text>"` before `--report` (bees-12.51).
