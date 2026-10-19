# Takes

One entry per take: what happened, what changed for the next one. **Take 7 is the one in
the README** (`docs/img/demo2.gif`, 86.6 s, 4.8 MB).

| Take | Verdict |
|---|---|
| 1 | Not recorded: the keeper moved capture off screen before it started. |
| 2 | Timed out in the studies on two driver bugs (label match, `done` ≠ idle); bs fine. |
| 3 | First full run (5 min). The GIF had a blank end card, a dropped piece, and the note on the wrong study. |
| 4 | Clean: the note is on zigzag and the panes are tight; the bee is still a scribble and the prompt noisy. |
| 5 | The zoomed bee and the plain prompt work; the zigzag only shows in motion, and the flower was lost. |
| 6 | Failed at the review: the studies were renamed, and review.sh only matched `STUDY.md`. |
| 7 | **Final.** The honey trail shows the zigzag in a still; it ends 🐝🌼 bzzzz!, and the files are named after the shapes. |

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

## Take 4 (off-screen, first through run.sh)

- 4 min 18 s real; GIF 79 s, 4.5 MB; `no leaks`. The note lands on zigzag's study, and the
  stacked panes now show only the work.
- The queen's review of take 3: the end card was an empty terminal; the keeper's starship
  prompt showed `(us-east-1)`, `via v3.13.7`; the bee was a scribble at agg's 14 px.
- **For take 5:**
  - The keeper pane runs zsh with its own ZDOTDIR: prompt `buzz $`, no starship.
  - The flight runs on a cleared pane and is cut from a second agg render at 28 px,
    cropped to the keeper pane's top rows.
  - The take ends on `git log --graph` plus the bee, also zoomed, and the end card holds it.

## Take 5 (off-screen)

- 90 s GIF at first (86.5 s re-cut), 4.5 MB, `no leaks`. The plain `buzz $` prompt works,
  and so does the zoom: the bee is big, and the end card holds the merge graph with the
  bee. That worker's build ended with the bee on the flower but no flower and no "bzzz":
  workers vary.
- **Re-cut from the same take:** the crop is 1.5 rows down (it caught herdr's tab bar)
  and 60×16 cells (17×84 left black bands); a few budgets trimmed to make 86.5 s.
- **For take 6:**
  - The zigzag lasted about 1.5 s of a 7 s stretch. The flight is now 3 s real, played at
    0.5×.
  - Line 2 asks for the studies as straight.md, zigzag.md and loop.md: the queen's review
    wanted names that mean something.
  - Line 2 asks the picked worker to build "following my note".

## Take 6 (off-screen)

- Failed at the review: line 2 now names the studies straight.md etc., and review.sh only
  matched titles ending `STUDY.md`. It now matches any `.md` in the sidebar. `no leaks`.

## Take 7 (off-screen, final)

- 4 min 38 s real; GIF 86.6 s, 4.8 MB; `no leaks`. The steer is now "leave a trail of
  honey drops (o) where the bee flew, and keep the flower next to it on the last frame",
  following the queen's review: the zigzag has to show in a still, and the last frame
  has to show the flower and the buzz. The worker kept the flower, added the trail and a
  `bzzzz!`, and wrote a test for both.
