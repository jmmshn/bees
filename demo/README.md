# Recording the bs demo

How to record a GIF of the whole bs loop with real herdr and real Claude sessions:
the keeper asks the queen for a feature, the queen files a bead and spawns a worker,
the worker commits and reports done, and the queen closes the bead and ends the worker.
The first take (bees-8.1) ran 176 s raw and rendered to an 86 s, 3.1 MB GIF at 1193×804.

[`record.sh`](record.sh) runs the take in steps. [`keeper.md`](keeper.md) holds the keeper's
lines. [`seed/`](seed/) is the toy project's starting code: `wc.py`, with `--top N` missing.
The `.cast` and `.gif` stay out of git.

## Prerequisites

- `asciinema` 3.x and `agg` (`brew install asciinema agg`), plus `jq`.
- herdr, Claude Code and `bd`, set up as in [docs/SETUP.md](../docs/SETUP.md).
- A shared `bs` on PATH, installed from `main` (`bs version` prints main's commit).
  The queen and the workers both run it.
- The **live-test skill** ([.claude/skills/live-test](../.claude/skills/live-test/SKILL.md)).
  `record.sh` uses its `setup.sh` and `teardown.sh` for the toy project, so it doesn't
  duplicate them. **Pending:** the skill comes with PR #52, which also adds the `bs end --all`
  that teardown needs. Until #52 is merged and installed, point `LIVE_TEST` at a checkout of
  the skill, and expect teardown to fail once the queen has written to the toy's logbook.

## Steps

Run these from the repo root, in a terminal outside herdr or inside it. `<out>` is a directory
for the take, e.g. `scratch/demo`.

1. **Start.** `demo/record.sh start <out>` starts a headless `asciinema rec` (140×40) of a
   fresh herdr session, `bees-demo`, launched with a clean environment, so its panes inherit
   none of your shell's `BS_*`, `CLAUDE_*` or `HERDR_*` variables. Inside that session it runs
   live-test's `setup.sh`: a toy repo `lt<id>` under `~/.cache/bees-live/`, a workspace, the
   queen started with `bs queen`, and the folder-trust prompt answered. Then it commits
   `seed/` into the toy and focuses the queen.
2. **Ask.** `demo/record.sh say <out> "<line 1>"` (from [keeper.md](keeper.md)). It returns
   once the queen has spawned the worker and is idle again.
3. **Show the worker.** `demo/record.sh look <out> 6` puts each worker's pane on screen for
   6 s, then the queen's. Run it straight after step 2: the worker took 18 s in the first take.
4. **Wait for done.** The worker's `bs send` rings the queen, which reports the branch on its
   own. Wait until the queen's pane shows it (`herdr --session bees-demo pane read <pane>`,
   with `<pane>` from `<out>/live.json`).
5. **Merge.** As the keeper, `demo/record.sh merge <out>` merges the worker's branch into the
   toy's `main`. There's no remote, so there's no PR.
6. **Close out.** `say` lines 2 and 3 from keeper.md: `bs status`, `bd close`, `bs end`, then
   the epic and `bs prune`.
7. **Stop.** `demo/record.sh stop <out>` detaches the herdr client, which ends the cast. It
   then runs live-test's `teardown.sh` (workers, workspace, beads, repo, logbook and a leak
   check, which must print `no leaks`), stops the session, and renders
   `agg --idle-time-limit 2 --font-size 14 --last-frame-duration 4` to `<out>/demo.gif`.

Retake a messy take rather than editing it: `stop`, then `start` again.

## Timing tips

- `--idle-time-limit 2` cuts each wait to 2 s, so the raw length barely matters. What counts
  is how many screens change. Three keeper lines came to 86 s.
- One keeper line per step. Give the worker its constraints in line 1 (commit on its branch,
  report the branch, no PR), or it will try to push.
- To check a frame without a video tool, cut the cast at time t: rewrite every event up to t
  to time 0, render that with agg, and convert the one-frame GIF with `sips -s format png`.

## Known flaws

- **bees-8.11:** `bs status` lists any session that ever wrote to the project's logbook, so a
  session that spawned in the toy from outside shows up on camera. live-test's fresh toy
  avoids it; the first take, in a reused project, showed `bees-8-1`.
- **The toy's name is on camera:** `queen-lt<16 digits>`. live-test needs that shape to tear
  down safely.
- **Folder trust:** `setup.sh` can send its keys before Claude reads them. `record.sh` retries
  them. The prompt shows on camera for a moment.
- **The queen's first reply** carries the doorbell's boilerplate block ("Another Claude
  session sent a message …"). That's Claude Code's own wording.
