# Takes

One entry per take: what happened, what changed for the next one.

## Take 1 (2026-10-19, full screen)

Nothing recorded: the keeper switched to off-screen capture before it started. See PLAN.md §1.

## Take 2 (off-screen)

- Beat 1 ran: 24 s from the question to three workers spawned. The studies took 28–46 s
  (commits at +52 s to +70 s), well under the 2-minute target. Every worker obeyed "no
  remote, no PR"; zigzag's report ended "I didn't push or open a PR".
- **Broke: take.sh waited 15 min and timed out.** Two bugs in the driver, none in bs:
  the queen labelled the workers "straight path study" etc., where take.sh looked for the
  exact label "straight"; and a worker that has finished shows herdr status `done`, not `idle`.
- Noise on camera: every toy session prints the keeper's global heartbeat line
  (`🫀 claude AGENTS.md loaded`), since `~/.claude/CLAUDE.md` loads in any repo.
- **Next:** `ws_of` falls back to a label containing the word; `idle` accepts `done`; line 1
  asks for the labels straight, zigzag and loop.

## Take 3 (off-screen)

- **First full run, 5 min 3 s end to end**: question → 3 workers 27 s; studies 59 s; review
  25 s; build with steer 51 s; merge, `branch -D`, close-out 66 s. No permission prompt,
  no PR attempt, the steer landed ("wider zigzag flight that ends with a buzz"), `no leaks`.
- GIF: 83 s, 4.5 MB (quality 65; 8.2 MB at 75).
- **Fixed in the edit, re-cut from the same take:**
  - The end card was blank. agg writes a frame only on a screen change, so a cut inside an
    idle stretch caught nothing, and the hold cloned the detach's cleared screen. Sources go
    to constant frame rate first.
  - edit.py renders each stretch to its own file, checks its length, then joins them and
    wipes. One big filter graph had silently dropped 4 s, then 43 s.
  - The ⏩ badge moved the caption text mid-wipe ("plaplannotator"); it now sits at the
    right edge.
  - The worktree and stacked-panes shots have captions of their own.
- **For take 4:**
  - The note went on the *straight* study. The queen copied the studies to
    `zigzag-STUDY.md` etc., so the branch name didn't match. review.sh now falls back to
    "zigzag".
  - The stacked panes spent 5 of their 14 lines on Claude's input box and status bar.
    paneshot.py drops them and keeps 16 lines.
