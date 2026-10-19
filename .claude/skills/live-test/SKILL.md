---
name: live-test
description: Live-test bs end to end in a throwaway project, from setup to a leak-checked teardown. Use when exercising a bs change live (real herdr, real Claude sessions), probing a bug, playing a bake-off card by hand, or recording a demo.
---

# Live-test bs

A live test plays the keeper against a real queen in a **toy** project, then tears the toy down. Done means: the check has its expected-vs-actual record, and `teardown.sh` exited 0 with `no leaks`. A leak (a bead, logbook row or mail in a real project; a workspace, worktree or file left behind) fails the test, whatever the check showed.

The toy's id (`lt<16 digits>`) names everything it owns: its project, beads (`<toy>-N`), logbook (`~/.local/share/bees/<toy>.db`), cache files (`<toy>-*`) and sessions (`queen-<toy>`, `<toy>-N`). Teardown finds what to remove from that id and the run's records, never from herdr's list.

## 1. Setup

```sh
go build -o bin/bs ./cmd/bs                    # the build under test, in this checkout
.claude/skills/live-test/setup.sh [<bin dir>]  # prints live.json: toy, run, repo, pane, workspace, builds
```

Setup makes a git repo at `~/.cache/bees-live/<toy>/<toy>` (outside every repo, so bees' `CLAUDE.md` stays out of the toy's sessions), opens a herdr workspace on it, starts `bs queen` there with your build first on its PATH, and answers the folder-trust prompt. Keep every file you write under `<run>/keeper/`; other live tests may run beside yours.

**Which build ran what.** The queen's bs calls run your build (`queen_bs` in live.json). Workers start from herdr's environment, so they run the shared `bs` on PATH (`worker_bs`). Say both in your report; to test a worker-side change, the shared bs must be that build, which is the queen's install, never yours.

**Stay in the toy.** Pass `--project <toy>` to every bs call of yours: a bare `bs` in this checkout acts on the real bees project, and an exported `BS_PROJECT` holds only inside the checkout your session was launched in (elsewhere bs refuses it). Leave `BS_LOGBOOK` unset: it reaches your shell and the queen, never the workers, who would then read another logbook. Both scripts unset both.

## 2. Drive

Play the keeper, through the queen's pane only (`pane` in live.json):

- Say a line: `herdr agent prompt <pane> "<line>"`, then `herdr agent wait <pane> --until working --timeout 20000`, then `herdr agent wait <pane> --timeout 600000`. Check `herdr pane read <pane> --source recent --lines 40` shows `❯ <line>`: a pre-filled suggestion can swallow it.
- One line per prompt. herdr refuses control characters in agent text, so multi-line text goes in a file under `<run>/keeper/`, and the line says `read <file>`.
- Read state from `bs status --project <toy>` and the toy's logbook (`~/.local/share/bees/<toy>.db`, read-only through `python3 -I` and `sqlite3` with `mode=ro`: the sandbox stops the `sqlite3` CLI opening it), never from panes, worktrees on disk or scrollback (motif 1).
- A worker stuck at a prompt: answer it in its pane, and log that as a workaround; it is evidence for `capture-pitfall`.

## 3. Record (optional, for demos)

```sh
asciinema rec <run>/keeper/take.cast -c "herdr"   # in a terminal sized for the GIF; drive from another shell
agg --idle-time-limit 2 <run>/keeper/take.cast <run>/keeper/take.gif
```

Recording the herdr client shows the queen's and workers' panes together. Retake a messy take rather than editing it.

## 4. Check

For each thing the change claims, write down the command, the expected output and exit code, then the actual ones (`echo "exit $?"` after each). The check passes only when every line matches.

A PR body gets a before/after transcript (AGENTS.md): the same steps run with the shared bs (before, the wrong output marked `← the bug`) and with your build (after), as a fenced queen ↔ worker chat, e.g. `queen-<toy> ▸ bs end --all --project <toy>`.

## 5. Teardown

```sh
.claude/skills/live-test/teardown.sh ~/.cache/bees-live/<toy>
```

In order, stopping at the first failure (exit 1; fix the cause and run it again, it is idempotent):

1. `bs end --all --project <toy> --force --forget --yes`: every worker the toy's logbook records as live, nothing else. `--forget` records the end of a worker whose workspace id herdr has reused, leaving that workspace open. It runs the shared bs, or your build when the shared one predates `--forget`, from `/` so no checkout's project or staleness hint gets in.
2. Closes the queen's workspace, the id setup recorded, only while a pane in it is in the run dir; then runs step 1 again, for a worker the queen spawned meanwhile.
3. Deletes every `<toy>-*` bead.
4. Removes the toy repo and its worktrees, moves the toy's Claude transcripts from `~/.claude/projects` into `<run>/keeper/transcripts/`, then removes its logbook and cache files. `<run>/keeper/` is kept for your records; delete it when you're done. Claude's folder-trust entry for the toy stays in `~/.claude.json`, which live sessions rewrite.
5. Checks for leaks (exit 2): panes in the run dir, toy files, rows naming the toy or written as `keeper` in any other logbook, and beads in other projects that name the toy.

## 6. Report

Exit 2 is a leak: run `capture-pitfall` on how it got there, then remove it by hand. Teardown can't see a bead the run filed elsewhere without naming the toy, so also look at `bs bead ls` in the project you work in. Report the check's expected-vs-actual lines, both builds, every workaround, and `no leaks`.
