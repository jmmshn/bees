# Demo 2: recording plan

No take runs until the keeper approves this plan.

## 1. What take 1 taught

Take 1 recorded nothing. The stop arrived before the take had started, so no beat ran
and there are no beat timings yet. **Take 2 doubles as the timing run.** What the prep did
teach:

- **Toy lifecycle works.** `setup.sh` takes about 25 s (repo, headless herdr session `buzz`,
  queen, folder trust, warm-up). `teardown.sh` ended `no leaks` both times it ran.
- **plannotator can be driven** over CDP with no extra installs. Click the study in the
  sidebar, then *Global comment*, type the note, *Add*, then *More decisions ▸ Approve with
  notes*. plannotator then prints `{"decision":"approved","feedback":"… zigzag, end with a
  buzz …"}` and exits. Plain *Approve* drops the note.
- **First-visit dialogs** ("Choose how plans look", "What's new") would land in the video.
  They're stored per origin in the browser, so a fixed `PLANNOTATOR_PORT=19480` plus a
  profile warmed once by `setup.sh` keeps them away (checked: only *Approve* is left).
- **Full-screen capture is out.** It catches the keeper's screen and needs hands off. A test
  also popped a macOS local-network dialog: my test server listened on every interface,
  where plannotator listens only on loopback.
- **ffmpeg from brew has no `drawtext`**, so captions are PNGs that headless Chrome
  renders and ffmpeg stacks under the picture. Looped caption inputs need `-framerate`
  pinned, or the frame count drifts (the first edit test came out 18 s instead of 7.5 s).

## 2. Storyboard (target ≈ 80 s)

| # | On screen | Caption | Final | Source |
|---|---|---|---|---|
| 1 | queen pane: question typed, `bs epic`, `bs bead new` ×3, `bs spawn` | 1 · keeper asks the queen for a design study; she fans it out to 3 workers | 8 s | cast, beat1→spawned, ≤4× |
| 2 | straight, zigzag, loop panes, 3 s each, mid-prototype | same | 8 s | cast, real time |
| 3 | queen: three `done` messages arriving | same, plus `⏩ N×` | 5 s | cast, studies wait, compressed |
| 4 | queen: `git log main..<branch>` ×3, plannotator starts | 2 · keeper reviews all three studies in plannotator and picks one | 5 s | cast |
| — | wipe left, terminal → browser | — | 0.6 s | xfade |
| 5 | plannotator: each STUDY.md 2 s, zigzag's, note typed, *Approve with notes* | same | 15 s | browser screencast |
| — | wipe right, browser → terminal | — | 0.6 s | xfade |
| 6 | queen: reads the decision, `bs send` build to zigzag, `bs end` ×2 | 3 · the worker that already has the context builds it; keeper steers mid-build | 5 s | cast |
| 7 | zigzag pane: steer typed mid-build | same | 6 s | cast |
| 8 | zigzag pane: build finishing, commit, `done` | same, plus `⏩ N×` | 5 s | cast, compressed |
| 9 | keeper shell: `git merge --no-ff`, `git log --graph`, `python3 buzz.py` (bee zigzags, bzzz) | 4 · plain git merge, and the bee flies | 10 s | cast, real time |
| 10 | queen: close-out (bd close, bs end, bs prune, branch -D) | same | 4 s | cast, compressed |
| 11 | keeper shell: the bee at the flower | end card: *bees: one queen, a fleet of Claude workers, all on local git* | 4 s | cast, last frame held |

## 3. Capture

- **Terminal.** `take.sh` starts `asciinema rec --headless --window-size 150x42 -c "herdr
  session attach buzz"` with a clean env, as `record.sh` (bees-8.1) did. 150×42 with agg at
  font size 14 comes out about 1260×800, so 1200 wide stays readable. agg renders the whole
  cast in real time (`--idle-time-limit 99999`); edit.py does all the retiming. The take's
  client is the only one attached: the keeper detaches theirs, since herdr may size panes to
  the smaller client.
- **Which pane is in the cast.** `herdr workspace focus <id>` by label (`queen`, `keeper`,
  and the worker labels `straight`/`zigzag`/`loop` that `bs spawn` sets). The attached
  client follows the focus; `record.sh look` relied on this in bees-8.1.
- **Keeper's typing.** `herdr pane send-text`, 4 characters every 30 ms, then Enter, so the
  lines visibly type. Waits poll `herdr pane list` (agent idle or working), `bs status
  --project buzz --json` (three workers), and `git cat-file -e <branch>:STUDY.md`.
- **Browser.** `PLANNOTATOR_BROWSER=stage.sh`. stage.sh starts a **headless** Chrome
  (nothing on screen) on the warmed profile at 1200×780, with DevTools on port 9223.
  `take.sh` records it with CDP `Page.startScreencast` (`cast.mjs`: JPEG frames, each with
  CDP's own wall-clock timestamp), then drives the review through `cdp.mjs`. A small
  injected cursor dot glides to each control before the click, since headless Chrome draws
  no pointer. This is CDP rather than Playwright because Playwright isn't installed (no
  `ms-playwright` cache, no npm or Python package), and CDP needs nothing new.
- **Timestamps.** `take.sh` writes `marks.tsv` with wall-clock seconds and a mark name
  (`beat1`, `spawned`, `wait`, `studied`, `browser`, `approved`, `beat3`, `steered`,
  `built`, `beat4`, `flew`, `closed`, `end`). `t0` is taken just before asciinema starts,
  and the browser frames carry wall-clock time, so both sources align on one clock (error
  < 0.3 s).

## 4. Edit (`edit.py`)

- Each stretch between marks gets the speed that fits its budget in the storyboard: never
  slower than real time, and over 2× it shows `⏩ N×` in the caption, so the speed-up is
  honest.
- Browser: frames go to a constant 12 fps through the concat demuxer (each frame held
  until the next), from page-ready to *approved* + 1 s. In and out: `xfade=wipeleft` and
  `wiperight`, 0.6 s, against the terminal frame at that moment.
- Captions: a 56 px bar under the picture, dark `#0d1117`, white 26 px semibold system
  font, centred. The beat number leads. The end card replaces the bar with the summary
  line over the last frame, held 4 s.
- Output: 1200 px wide, about 830 px tall, 12 fps; `demo.mp4` (x264, crf 20) and
  `demo.gif` (gifski, quality 70–80). Budget **≤ 8 MB** (demo 1: 3.1 MB for 86 s). If over,
  drop to 10 fps, then quality 60.

## 5. Risks and fallbacks

- **A worker pushes or opens a PR.** There's no remote, so it fails, but it costs time and
  puts an error on screen. The bs worker prompt says "open a PR" (likely bs snag: bead
  under bees-20). The line says "No remote, no PR". If it happens anyway, edit.py cuts it,
  and I file the bead.
- **Slow studies.** Waits time out at 15 min. Any wait compresses, so a slow study only
  makes the take longer. Over 5 min, I note it and keep the take.
- **plannotator gate.** If the queen runs it in the foreground, Claude's 2-min Bash timeout
  could kill it, so the line says "in the background", and take.sh approves within about
  40 s of the page loading. If no page appears in 5 min, take.sh aborts, and I tear down.
- **Permission prompts** (auto mode gating plannotator, `bs spawn`, `git branch -D`): a pane
  going `blocked` aborts the take with the pane's text in `take.log`. I never answer one for
  the keeper. I report it to the queen; the fix is better wording or the keeper's okay.
- **Pane resizing** by a second attached client: the keeper detaches. Check the first frame.
- **On-screen noise**: the doorbell's boilerplate block in the queen pane. It's Claude
  Code's text, so the edit compresses past it.

## 6. Judging a take

I extract frames at the middle of every shot, at each caption change, at the middle of
each wipe, and at the end card, then look at each with Read. "Clear to a stranger" means
that from the frames alone, with no narration, one can answer:

1. What did the keeper ask? (The question is readable in shot 1.)
2. How many workers ran, and with what approaches? (Three labelled panes in shot 2.)
3. What did the keeper pick, and how? (The note "zigzag, end with a buzz" readable in shot 5.)
4. What changed mid-build? (The steer readable in shot 7.)
5. Did it work? (The merge graph and the bee at the flower, shots 9 and 11.)

And: every caption is readable at 1200 px; the active pane's text is legible (glyphs ≥ ~9
px tall); no permission prompt, error or blank pane is held over 1 s; no frame repeats
over 2 s outside the end card; 60–90 s; ≤ 8 MB. Each take's changes go in `TAKES.md`.
