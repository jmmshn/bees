# Demo 2: the bee design study

The keeper's run sheet. A queen fans a design question out to three workers, the keeper
picks one in plannotator, that worker builds it with a steer mid-build, and the keeper
merges it. Local git all the way: no remote, no PR, no `gh`.

The toy is `~/demo/buzz` (project `buzz`), in a herdr session of its own (`buzz`), so the
keeper's own herdr and the real bees project stay out of the picture. Its `main` holds
[`buzz.py`](buzz.py), which prints a bee that doesn't move: `🐝 . . . . . 🌼`.

Worker names follow their beads: bead `buzz-1.2` is worker and branch `buzz-1-2`, in the
worktree `~/demo/buzz-WT/buzz-1-2`. Which number is which shape depends on the order the
queen files them; read it from `bs status` or the queen's reply.

## Before the take

1. **Setup**, from this repo: `demo/demo2/setup.sh`. It refuses to run over an earlier
   run's leftovers (run `teardown.sh` first). It makes the toy repo, starts the herdr
   session `buzz` headless with a clean environment, opens two workspaces (`queen`, running
   `bs queen`, and `keeper`, a shell in `~/demo/buzz`) and answers the folder-trust prompt.
   It ends with `ready: queen-buzz in pane …`.
2. **Terminal**: in cmux, `herdr session attach buzz`, then put that window in macOS full
   screen (View ▸ Enter Full Screen, or ⌃⌘F). It now has a Space of its own.
3. **Browser**: nothing to do. setup.sh gives the toy's sessions
   `PLANNOTATOR_BROWSER=demo/demo2/stage.sh`. When the queen runs plannotator, stage.sh
   opens the review page in a Chrome instance of its own (profile under `~/demo/.stage`),
   in kiosk mode: full screen, no tabs or toolbar, in a new Space, so the screen slides
   over to it. Once you approve, plannotator's server stops, stage.sh quits that Chrome,
   its Space closes and the screen slides back to the terminal.
4. **Recording** (not for the dry run): see [Recording](#recording).

## Beat 1: the question

On screen: the terminal, `queen` workspace. Type to the queen:

```text
Design study: how should the bee reach the flower? Three workers: straight, zigzag, loop. Each prototypes it in buzz.py and writes STUDY.md (how it looks, trade-offs, open questions), commits both on its branch and reports done with the branch name. No remote, no PR. Don't build yet.
```

Wait for: `bs epic`, three `bs bead new`, one `bs spawn --label straight … --label zigzag
… --label loop …`. Three workspaces appear in herdr's sidebar. Flick through them if you
like; come back to `queen`.

## Beat 2: the review

Wait for: three `done` messages. Each rings the queen, which relays the branch. When all
three are in, type:

```text
For each branch show me git log --oneline main..<branch>, then run plannotator annotate on the three STUDY.md files with --gate --json in the background, and wait for my decision.
```

Wait for: three short logs, then the screen slides to the browser: the three studies as
one review. **On screen: the browser.** Read, annotate if you like, then **Approve** with a
note, e.g.:

```text
zigzag, end with a buzz
```

The screen slides back to the terminal. The queen reads the JSON and picks.

## Beat 3: the build

Wait for: the queen to `bs send` the zigzag worker `build it: …` and `bs end` the other
two (their workspaces leave the sidebar).

Switch to the zigzag workspace while it builds, and type a steer into its pane:

```text
make the zigzag a bit wider
```

Wait for: the worker to commit and `bs send` done. Switch back to `queen`; it relays the
branch.

## Beat 4: merge and close out

Switch to the `keeper` workspace (a shell in `~/demo/buzz`). With `<branch>` the zigzag
worker's:

```sh
git merge --no-ff <branch>
git log --oneline --graph
python3 buzz.py
```

The bee zigzags to the flower and buzzes. Switch to `queen` and type:

```text
Close it out: bd close the beads and the epic, bs end the zigzag worker, bs prune, and git branch -D the two unmerged study branches.
```

Wait for: `bs prune` deleting the merged branch and two `Deleted branch …` lines.

## After the take

```sh
demo/demo2/teardown.sh
```

It ends any worker still running, stops and deletes the herdr session `buzz`, deletes the
`buzz-*` beads, moves the toy sessions' Claude transcripts to `~/.cache/bees-demo2/<time>`,
removes `~/demo/buzz`, its worktrees, `~/demo/.stage`, the toy's logbook and cache files,
and checks for leaks. It must end `no leaks`. It removes `~/demo` only if empty, so a
recording saved there stays. Claude's folder-trust entry for `~/demo/buzz` stays in
`~/.claude.json`.

## Recording

The take is the whole display, Space switches included, so record the screen, not a
window:

- **`screencapture -v ~/demo/take.mov`** from a shell, before Beat 1 (`-V <secs>` to cap
  it; Ctrl-C stops it). It needs **Screen & System Audio Recording** permission for the
  app the shell runs in (System Settings ▸ Privacy & Security). cmux has it: granted and
  checked 2026-10-19 with `screencapture -x`, which before the grant failed with `could
  not create image from display`.
- **QuickTime**: File ▸ New Screen Recording (or ⇧⌘5), "Record Entire Screen". QuickTime
  asks for the same permission for itself the first time.

Either way the macOS menu bar and notifications can land in the take: turn on Do Not
Disturb first.
