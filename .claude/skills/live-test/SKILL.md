---
name: live-test
description: Live-test bs end to end in a throwaway project, from setup to a leak-checked teardown. Use when exercising a bs change live (real herdr, real Claude sessions), probing a bug, playing a bake-off card by hand, recording a demo, or sweeping old test leftovers (run dirs, toy logbooks, beads, start files).
---

# Live-test bs

A live test plays the keeper against a real queen in a **toy** project, then tears the toy down. Done means: the check has its expected-vs-actual record, and `teardown.sh` exited 0 with `no leaks`. A leak (a workspace, pane, worktree, bead or file of the toy's left behind) fails the test, whatever the check showed. Leftovers outside the toy (a bead or logbook row the toy's sessions wrote into a real project) are not leaks: teardown notes them, and they get cleaned later.

The toy's id (`lt<16 digits>`) names everything it owns: its herdr session, its config dir (`~/.config/bees/sessions/<toy>/`), project, beads (`<toy>-N`), logbooks (`<run>/logbooks/`), cache files (`<toy>-*`) and sessions (`queen-<toy>`, `<toy>-N`). Teardown finds what to remove from that id and the run's records, never from herdr's list.

## 1. Setup

```sh
go build -o bin/bs ./cmd/bs                    # the build under test, in this checkout
.claude/skills/live-test/setup.sh [--no-queen] [--harness agy] [--cl] [<bin dir>]   # prints live.json
```

Setup:

- makes a git repo at `~/.cache/bees-live/<toy>/<toy>` (or under `$BEES_LIVE`, symlinks resolved), outside every repo so bees' `CLAUDE.md` stays out of the toy's sessions, with git `user.name` set to the toy id (bd's `created_by` on every bead the toy's sessions file, so teardown can note one filed elsewhere);
- writes the toy's config, `~/.config/bees/sessions/<toy>/config.toml`, with `paths.logbook_dir = <run>/logbooks`. `bs` in a pane on herdr session `<toy>` reads that dir and nothing else of `~/.config/bees` (ADR-0010). Edit it, or add `projects/<toy>.toml`, before your check;
- starts the toy's own herdr server, `herdr --session <toy> server`, from `env -i` plus `PATH` (your build first), `HOME`, `TERM`, `LANG` and `SHELL`, plus any `XDG_*` you have set: no `BS_*` or `HERDR_*` of your shell reaches its panes. XDG is left as you have it (a separate XDG home broke the keeper's shell and overflowed `sun_path`), so the toy reads the config dir setup wrote;
- opens a workspace there on the repo, and records in live.json which `bs` its shell runs (`pane_bs`): your shell's rc runs in the pane and may put dirs ahead of your build;
- with `--harness agy`, checks agy is signed in from the toy's env (agy reads `SSH_CONNECTION`/`SSH_CLIENT`/`SSH_TTY` as a remote session and is "not signed in" there; the toy's bare env drops them, and bs unsets them in an agy worker's pane before its launch; `LIVE_SSH=1` hands them to the toy's panes, as a herdr server started over ssh does), warns when agy's `permissions.allow` lacks `command(bs)`, writes `AGY_MODEL` as `[worker.agy] model`, and records agy's settings path (`AGY_SETTINGS`) and version. The queen stays on Claude Code: tell it to `bs spawn --harness agy …`. agy prompts for each command and file write outside `command(bs)`: answer them in the worker's pane, and log each as a workaround;
- with `--cl`, makes the toy CL-shaped ([cltoy/](cltoy/README.md)): `cl`, a small script that is both the monorepo client tool (a client is a git clone of the toy repo) and the CL host, goes on every toy pane's PATH, and the toy's config gets a `[workspace.command]` with its VCS keys and `review.host = "command"` over it, so `--goal pr` runs spawn, `cl upload`, `bs done`, `cl submit`, `bs end` and `bs prune` with nothing internal. Teardown removes the tool, its state and the clients;
- unless `--no-queen`, starts `bs queen` in that pane with your build first on its PATH, answers the folder-trust prompt, and exits 0 only once the queen is up and idle.

Keep every file you write under `<run>/keeper/`; other live tests may run beside yours.

**Which build ran what.** The queen's bs calls run your build (`build` in live.json). Workers start from the toy server's env, after the pane's shell rc: `pane_bs` says what they run. Say both in your report.

**Stay in the toy.** Every herdr call of yours names the toy's server: `herdr --session <toy> …`. A bare `herdr` is the keeper's server. Run `bs` from a toy pane, or from your shell with `HERDR_SESSION=<toy>` set and the toy repo as cwd: without it bs reads the keeper's own config and the real logbooks. bs takes its caller for its nearest recorded ancestor, so a worker's own shell is that worker, acting in the toy as another project's session: its `--as` is refused. A worker drives the keeper's side (`contract.sh` included) from a toy pane, whose server no session of yours started: `herdr --session <toy> pane run <pane> "bash <script> > <run>/keeper/<name>.out 2>&1"`. bs finds the project from the repo it runs in, and refuses an exported `BS_PROJECT` naming another project (from the keeper's shell; under a recorded session every `BS_*` is ignored, with a warning).

**Never delete or mutate by hand** under `~` or `$HOME` (`rm`, `mv`, `sqlite3` writes): each Bash call starts a fresh shell, so a `HOME` or path you exported earlier is gone and `$HOME` is the real one. That is how a review agent deleted the real `bees.db` (bees-8.12). `teardown.sh` is the only deleter, and it removes only absolute paths named after the toy. A scratch file of yours goes under `<run>/keeper/`, by its absolute path. A check that needs the config dir gone moves it into `<run>/keeper/` and back, by absolute paths named after the toy.

## 2. Drive

Play the keeper, through the queen's pane only (`pane` in live.json):

- Say a line: `herdr --session <toy> agent prompt <pane> "<line>"`, then `herdr --session <toy> agent wait <pane> --until working --timeout 20000`, then `herdr --session <toy> agent wait <pane> --timeout 600000`. Check `herdr --session <toy> pane read <pane> --source recent --lines 40` shows `❯ <line>`: a pre-filled suggestion can swallow it.
- Run a shell command in a toy pane: put it in a script under `<run>/keeper/` and `herdr --session <toy> pane run <pane> "bash <script> > <run>/keeper/<name>.out 2>&1"`, then read the file.
- One line per prompt. herdr refuses control characters in agent text, so multi-line text goes in a file under `<run>/keeper/`, and the line says `read <file>`.
- Read state from `bs status` (with `HERDR_SESSION=<toy>`) and the toy's logbook, through `python3 -I .claude/skills/live-test/logbook.py <run>/logbooks/<toy>.db "<select>"` (it queries a stable private copy, so the live logbook gets no write and no `-wal`/`-shm` files), never from panes, worktrees on disk or scrollback (motif 1).
- A worker stuck at a prompt: answer it in its pane, and log that as a workaround; it is evidence for `capture-pitfall`.

## 3. Record (optional, for demos)

```sh
asciinema rec <run>/keeper/take.cast -c "herdr --session <toy>"   # in a terminal sized for the GIF; drive from another shell
agg --idle-time-limit 2 <run>/keeper/take.cast <run>/keeper/take.gif
```

Recording the toy's herdr client shows the queen's and workers' panes together. Retake a messy take rather than editing it.

## 4. Check

For each thing the change claims, write down the command, the expected output and exit code, then the actual ones (`echo "exit $?"` after each). The check passes only when every line matches.

A change to spawn, done, end, goal, prune or identity also replays the contract card, [CONTRACT.md](CONTRACT.md): its Go half runs in `go test` (`-run TestContract -v` shows each case), and `contract.sh <run dir>` on a toy from `setup.sh --no-queen` runs the same cases live and prints PASS/FAIL per case (XFAIL/XPASS for a rebuild ticket not landed yet), with a table to paste into the PR body.

An agy-compatible CLI (agy itself, or another build of its codebase) is probed without a toy: `agy-probe.sh <binary>`, from your own signed-in shell. In one throwaway run dir, it spends 3 model turns (`AGY_MODEL` picks a cheap model) and starts the TUI 3 times with no turn. It prints a PASS/FAIL/SKIP table, one row per feature the agy adapter relies on: the agent file and hooks loading from `--add-dir`, the hook decision format (empty stdout means no decision, `{}` reads as deny), resume by `--conversation`, exact-path trust, and the env var names a hook and a `run_command` child get. It removes the trust entries it caused (`--keep-trust` keeps them) and lists what the binary kept. The table names no binary, path or model, so a user can share it; bs ships the script, never a table from a build that isn't public. `run_command` needs `command(bs)` in the CLI's `permissions.allow`, which bees needs anyway; without it that row is SKIP.

A PR body gets a before/after transcript (AGENTS.md): the same steps run with the shared bs (before, the wrong output marked `← the bug`) and with your build (after), as a fenced queen ↔ worker chat, e.g. `queen-<toy> ▸ bs end <toy>-1`.

## 5. Teardown

```sh
.claude/skills/live-test/teardown.sh ~/.cache/bees-live/<toy>
```

In order, stopping at the first failure (exit 1; fix the cause and run it again, it is idempotent):

1. `herdr --session <toy> server stop`, then `herdr session delete <toy>`: the toy's server holds every pane and process the toy started, and is named after it, so nothing of the keeper's is touched (motif 12). No `bs end`: the logbook recording the ends is deleted below.
2. Deletes every `<toy>-*` bead. It stops instead if one is linked to a bead outside the toy, since `bd delete` would rewrite that bead's references to `[deleted:ID]`.
3. Removes the toy repo and its worktrees, moves the toy's Claude transcripts from `~/.claude/projects` into `<run>/keeper/transcripts/`, then removes its logbooks, cache files and config dir. Of agy's files, it removes the `trustedWorkspaces` entries in the run dir (under the lock bs takes), and moves into `<run>/keeper/agy/` the logs of agy starts whose workspace is in the run dir and the conversations they created (`conversations/`, `brain/`, `annotations/`, `implicit/`); lines in agy's shared `history.jsonl` and `cache/last_conversations.json` are noted, not edited. `<run>/keeper/` is kept for your records; delete it when you're done. Claude's folder-trust entry for the toy stays in `~/.claude.json`, which live sessions rewrite.
4. Checks for leaks (exit 2): the toy's herdr session still listed, a pane of the keeper's herdr in the run dir (or herdr unreachable, so it can't tell), a process still running in the run dir (where there is no `/proc`, as on macOS, it says it can't check, and ends with `no leaks found; not checked: stray processes`), and toy files.
5. Prints a `note:` line, never an exit code, for each leftover outside the toy: beads with `created_by` the toy id outside `<toy>-*`, and rows the toy's sessions (`queen-<toy>`, `<toy>-N`) wrote into other logbooks (read through `logbook.py`). Last, it compares the keeper's workspaces with the list setup took: all still open, or a note naming the ones closed since (never by teardown). This is a report, not a check: it never fails teardown.

## 6. Report

Exit 2 is a leak: run `capture-pitfall` on how it got there, fix the cause, and rerun teardown; don't remove it yourself (see "Never delete or mutate by hand"). Copy any `note:` lines into your report so the leftovers can be cleaned later. Report the check's expected-vs-actual lines, both builds, every workaround, and `no leaks`.

## 7. Sweep old leftovers

Teardown keeps `<run>/keeper/`, and bake-off runs keep everything, so test leftovers pile up. `sweep.py` lists them, and with `--apply` removes them:

```sh
python3 -I .claude/skills/live-test/sweep.py [--days 7] [--apply]
```

It takes run dirs under `$BEES_LIVE` and `$BAKEOFF_RUNS` untouched for `--days` (a live-test run only after teardown), after stopping their toy queens (a keeper workspace labelled `<toy> · …` with every pane under a test cache); then toy logbooks and `<toy>-N` beads whose run dir is gone, and bs start files (`~/.cache/bees/bs/<session>-prompt-*`) whose session's logbook shows it ended. A toy-shaped name is not proof (a real project may be `t2cache`): a logbook goes only if every repo its spawns recorded is the toy's in a test cache, a bead only if a toy created it (`bakeoff`, or the toy id), and never a configured project's. A toy whose queen or herdr server it can't prove and stop keeps everything, and it stops (exit 1) when it can't ask herdr which toys still run. Run it bare first and read the list.
