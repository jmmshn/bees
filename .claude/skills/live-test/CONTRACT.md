# The contract card

Every case of the keeper's 2026-10-22 post-swap pilot, plus the rebuild's new behaviors, as a replay of the handoff, end, done, prune and identity contract (ADR-0010 Amendment 4 is the spec; bees-13.67 §4 is the ticket list). It runs at two speeds:

- **Offline, in Go**: `internal/scenario/contract_test.go`, every case over the fakes. `go test ./internal/scenario -run TestContract -v` prints each case as a subtest named by its id. It runs in CI with the rest of `go test ./...`.
- **Live**: `contract.sh`, the offline tier against real herdr and real Claude sessions, on a toy from `setup.sh --no-queen` (below).

## Expected-fail

A case of behavior not built yet names its rebuild ticket: `xfail(t, "T3")` in Go, `xfail=T3` in `contract.sh`. Failing, it is `XFAIL (T3)` (a skip in Go, saying why); passing, it is `XPASS (T3 landed? drop its xfail mark)`. Neither fails the card. The ticket that builds the behavior drops the mark in both places, so the case guards it from then on: run `go test ./internal/scenario -run TestContract -v | grep -E 'XPASS|XFAIL \(T<n>\)'` before handing off.

| ticket | its cases |
|---|---|
| T1 identity and permissions; logbook creation | `amend-as-worker`, `as-check-other`, `as-check-own`, `as-end-as-worker`, `as-inbox-peeks`, `discard-as-queen`, `discard-as-queen-by`, `ident-as-other`, `ident-check-other`, `ident-end-incomplete-sibling`, `ident-end-sibling`, `ident-outside-reads-keeper`, `ident-prune`, `ident-tripwire`, `parallel-first-spawns`, `status-first-line-keeper`, `status-first-line-worker` |
| T2 cross-project send starts the queen | `foreign-end`, `foreign-send-starts-queen`, `foreign-sends-race-one-queen`, `foreign-spawn`, `queen-reply-to-worker`, `send-no-logbook` |
| T4 one goal per spawn | `amend-two-values`, `mixed-refuses-amend`, `mixed-refuses-done`, `spawn-two-bad`, `spawn-two-goals` |
| T5 stacked `pr=`, narrow `push=` | `pr-target-closed`, `pr-target-fork`, `pr-target-merged`, `push-discarded-pruned`, `push-own-branch`, `push-owned-refused`, `stacked-cut`, `stacked-handoff-after-target-merged`, `stacked-lower-end-refused`, `stacked-merged-into-live-target`, `stacked-prune-falls-back-to-trunk` |

Every other case holds today and passes plainly.

## What Amendment 4 changed on the pilot's card

- `verdict-pr`: `bs done --pr` is an unknown flag (exit 2) on every goal: the worker names nothing (T3).
- `mixed-*` is gone: a second `--goal` is refused (`spawn-two-goals`, exit 2, T4). A mixed goal recorded before then still decodes, shows in status and prunes on its union (`mixed-status`, `mixed-ends-and-prunes`), but can't be amended or handed off (T4).
- `spawn-two-bad` names the second `--goal`, not both values' errors; one bad value is `spawn-bad-value`.
- `ident-*` run from the worker's own tree: `end S1`, `end --incomplete S2`, `prune`, `goal S1` and `--as S1` are each exit 2 (T1; `goal` already holds).
- `discard-as-queen`: the queen never discards (exit 2, T1), and `--as` from the keeper's shell takes the named session's role.
- The pilot's GitHub tier is now fake-gh scenarios (`TestContractFindTheChange`, `TestContractStackedAndPush`, `TestContractReviewPostedByBs`, `TestContractStuckOnAClosedPR`). Its live run is on the sandbox repo `jmmshn/bees-toy-1306-10221643` (OQ-K4); each PR it opens is titled `[pilot] do not merge` and closed at the end.
- `gate-sleep-stopped` stays live-only: the background job dies with the real pane's pty, which the fake herdr doesn't model.

## The live run

```sh
go build -o bin/bs ./cmd/bs
.claude/skills/live-test/setup.sh --no-queen         # no queen: nothing reacts to the workers' mail
.claude/skills/live-test/contract.sh <run dir>       # ~5 min; prints a line per case
.claude/skills/live-test/teardown.sh <run dir>       # the only deleter, as always
```

`contract.sh` prints `PASS`, `FAIL`, `XFAIL` or `XPASS` per case with the expected and actual exit code and output, writes the same as a markdown table to `<run>/keeper/contract/table.md` (paste it into the PR body), keeps each case's output in `<run>/keeper/contract/out/<case>.txt`, and exits 1 on any FAIL. It runs once per toy.

It drives workers as the pilot did: one idle worker per group (`bs spawn --goal <goal>` on a bead whose brief says "do nothing: say idle and stop"), on `haiku` with the build first on its PATH (`projects/<toy>.toml`: `worker.path`, `worker.claude_code.model`). The keeper's shell plays the worker and the queen (`bs done --as <worker>`, `bs end --as queen-<toy>`), with `HERDR_SESSION=<toy>` and the toy repo as cwd. Two groups need code in a worker's pane, as a descendant of its harness: the gate (`herdr agent prompt` asks the worker to run `sleep 900` in the background) and identity (the command goes into the pane as Claude's `! <cmd>` shell, because the auto-mode classifier refuses the same command asked for in a prompt).

The first worker spawns alone and the rest together: concurrent first spawns race the logbook's creation until T1 (`parallel-first-spawns`). The card commits a `.gitignore` (`bin/`, `scratch/`) to the toy's main before spawning, and the amend group adds an `origin` remote naming a GitHub repo that is never contacted.
