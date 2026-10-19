# Demo 2: the bee design study

A queen fans a design question out to three Claude workers, the keeper picks one in
plannotator, that worker builds it with a steer mid-build, and the keeper merges it. Local
git all the way: no remote, no PR, no `gh`. The plan and storyboard are in
[PLAN.md](PLAN.md); what each take taught is in [TAKES.md](TAKES.md).

## Run it

```sh
demo/demo2/run.sh ~/demo/takes/<n>     # about 5 min; leaves demo.gif and demo.mp4 there
```

Everything happens off screen, so the Mac stays usable while it runs. Before a take, detach
any client of the herdr session `buzz`: herdr may size panes to the smallest client. To
watch one live anyway: `herdr session attach buzz`.

`run.sh` is four steps, each of which also runs alone:

1. **`setup.sh`** makes the toy: `~/demo/buzz`, a git repo whose `main` holds
   [`buzz.py`](buzz.py) (`🐝 . . . . . 🌼`, a bee that doesn't move), and the herdr
   session `buzz`, started headless with a clean environment. The session has two
   workspaces: `queen`, running `bs queen`, and `keeper`, a zsh with a plain `buzz $`
   prompt. It also warms plannotator's browser profile, so first-visit dialogs stay off
   camera. It refuses to run over leftovers; run `teardown.sh` first.
2. **`take.sh <out>`** plays the keeper. It types the lines into the panes, switches
   workspaces so the recorded client shows the action, and waits on bs, herdr and git
   state. It records:
   - `term.cast`: asciinema of a headless `herdr session attach buzz`, 150×42.
   - `pane-<label>.cast`: each worker's pane while they study (`paneshot.py`).
   - `browser/`: plannotator's page. `review.sh` drives it over CDP in the headless
     Chrome that `stage.sh` opens, with frames recorded by `cast.mjs`.
   - `marks.tsv`: wall-clock times of every beat, for the cut.

   A pane that blocks on a permission prompt stops the take, with the pane's text in
   `take.log`. take.sh never answers prompts.
3. **`teardown.sh`** ends the workers, stops and deletes the session, deletes the
   `buzz-*` beads, and moves the toy sessions' Claude transcripts to
   `~/.cache/bees-demo2/<time>`. It then removes `~/demo/buzz`, its worktrees,
   `~/demo/.stage`, and the toy's logbook and cache files, and checks for leaks. It must
   end with `no leaks`. Claude's folder-trust entry for `~/demo/buzz` stays in
   `~/.claude.json`.
4. **`edit.py <out>`** cuts the GIF. Each stretch between two marks is fitted into its
   budget, and a ⏩ badge shows the speed when it's over 2×. The browser part wipes in and
   out, the flight and the finale are zoomed, and each beat gets a caption. A title card
   opens and an end card closes. Each piece is rendered alone and its length checked.
   Tune the budgets and captions in `STRETCH` and `CAPTIONS`.

## The keeper's lines

What take.sh types, in order:

| Beat | Where | Line |
|---|---|---|
| 1 | queen | Design study: how should the bee reach the flower? Three workers, labelled straight, zigzag and loop. Each prototypes it in buzz.py and writes STUDY.md (how it looks, trade-offs, open questions), commits both on its branch and reports done with the branch name. No remote, no PR. Don't build yet. |
| 1 | keeper | `git worktree list` |
| 2 | queen | For each branch show me git log --oneline main..\<branch\>. Then copy the three STUDY.md files to straight.md, zigzag.md and loop.md in a temp folder and run plannotator annotate on those with --gate --json in the background. When I decide, have the worker I pick build it, following my note, and end the other two. |
| 2 | plannotator | open each study; on zigzag's, a global comment "zigzag, end with a buzz"; *More decisions ▸ Approve with notes* |
| 3 | zigzag | make the zigzag a bit wider |
| 4 | keeper | `git merge --no-ff <zigzag branch>`, `git log --oneline --graph`, `python3 buzz.py`, `git branch -D <the two study branches>` |
| 4 | queen | Close it out: bd close the beads and the epic, bs end the zigzag worker, then bs prune. |

## Needs

herdr, Claude Code, `bd`, and a shared `bs` installed from main. Also asciinema 3,
`agg` ≥ 1.9, ffmpeg, gifski, Google Chrome, node ≥ 22 (built-in WebSocket), jq,
plannotator, and python3.

## Recording the screen instead

The takes don't need it, but if a full-screen recording is ever wanted:
`screencapture -v <file>.mov` (Ctrl-C stops it) or QuickTime ▸ New Screen Recording. Both
need **Screen & System Audio Recording** permission for the app they run from (System
Settings ▸ Privacy & Security). cmux has it: granted 2026-10-19, and before that
`screencapture` failed with `could not create image from display`.
