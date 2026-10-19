#!/usr/bin/env python3
"""edit.py <out dir>: cut a take (take.sh) into the demo GIF.

Inputs in <out>: marks.tsv (wall-clock seconds, then a mark), term.cast (asciinema, the
herdr client), pane-<label>.cast (paneshot.py, each worker's pane while they study),
workers.tsv, and browser/ (review.sh's screencast frames with their wall-clock times).
Writes term.gif and pane-*.gif (agg, real time), browser.mp4, demo.mp4, demo.gif and
edit.tsv (the cuts). Delete a .gif to re-render it.

Each stretch between two marks plays at the speed that fits it into its budget, never
slower than real time; past 2x its caption says so. A title card opens, the browser part
wipes in and out, and an end card holds the last frame. A caption bar per beat, rendered
by headless Chrome, sits under the picture.
"""

import hashlib
import html
import json
import subprocess
import sys
from pathlib import Path

W, H, BAR = 1200, 780, 56  # picture, then the caption bar under it
FPS = 12
WIPE = 0.6
TITLE_LEN, TAIL = 2.5, 4  # seconds of the title card and of the end card
ROW_BAR = 26  # the label over each worker's pane in the stacked shot
CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
BG = "0x0d1117"

TITLE = ("bees", "a queen runs a design study with 3 Claude workers")
CAPTIONS = {
    1: "1 · keeper asks the queen for a design study; the queen fans it out to 3 workers",
    2: "2 · keeper reviews all three studies in plannotator and picks one",
    3: "3 · the worker that already has the context builds it; keeper steers mid-build",
    4: "4 · plain git merge, and the bee flies",
}
END = ("bees: one queen, a fleet of Claude workers, all on local git", "github.com/jmmshn/bees")
# The stretch starting at each mark: its budget in output seconds, and its beat (None: the
# beat it is in). The browser stretch runs from "browser" to "terminal"; "tiled" to
# "untiled" shows the three workers' panes stacked.
STRETCH = {
    "start": (1, 1),
    "beat1": (7, 1),  # the question typed; the queen files beads and spawns
    "spawned": (5, 1),  # keeper: git worktree list
    "branches": (0.5, 1),
    "tiled": (8, None),  # the three workers studying
    "untiled": (1.5, 1),
    "studied": (1, 1),
    "beat2": (5, 2),  # the queen's git logs; plannotator starts
    "browser": (14, 2),  # the review
    "terminal": (5, 3),  # the queen picks, sends the build, ends two
    "beat3": (6, 3),  # the steer
    "steered": (1, 3),
    "wait": (3, None),
    "built": (2, 3),
    "beat4": (9, 4),  # merge, graph, the bee
    "flew": (3, 4),  # branch -D the two studies
    "pruned": (2, 4),  # the queen's close-out
    "closed": (3, 4),  # the bee again
}


def run(*cmd):
    r = subprocess.run(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
    if r.returncode:
        sys.exit(f"{cmd[0]} failed:\n{r.stderr[-3000:]}")


def card(path, body, height):
    """A PNG of an HTML body, W wide, rendered by headless Chrome."""
    page = path.with_suffix(".html")
    page.write_text(f"<html><body style='margin:0;background:#0d1117;height:{height}px;display:flex;"
                    "flex-direction:column;align-items:center;justify-content:center;color:#f0f6fc;"
                    f"font-family:-apple-system,Helvetica,sans-serif'>{body}</body></html>")
    # A profile of its own: on the default one, headless Chrome can hang behind the keeper's.
    run(CHROME, "--headless=new", f"--user-data-dir={path.parent / 'cards-chrome'}", "--hide-scrollbars",
        f"--window-size={W},{height}", f"--screenshot={path}", page.as_uri())
    return path


def bar_body(text):
    if isinstance(text, tuple):
        return (f"<div style='font-weight:600;font-size:20px'>{html.escape(text[0])}</div>"
                f"<div style='font:500 17px ui-monospace,Menlo,monospace;color:#f5c542;margin-top:2px'>"
                f"{html.escape(text[1])}</div>")
    return f"<div style='font-weight:600;font-size:25px'>{html.escape(text)}</div>"


def fit(w=W, h=H):
    """Scale a stream to fit w×h and pad it onto a canvas of that size."""
    return (f"scale={w}:{h}:force_original_aspect_ratio=decrease:flags=lanczos,"
            f"pad={w}:{h}:(ow-iw)/2:(oh-ih)/2:color={BG},fps={FPS},format=yuv420p,setsar=1")


class Graph:
    """ffmpeg inputs and filter_complex, built up together."""

    def __init__(self, out):
        self.out, self.inputs, self.filters, self.n = out, [], [], 0

    def input(self, *args):
        self.inputs += [*args]
        self.n += 1
        return f"{self.n - 1}:v"

    def image(self, path, secs):
        """A still image as a stream secs long: one input per use, since an endless loop
        feeding a filter that has stopped reading spins ffmpeg forever."""
        return self.input("-loop", "1", "-framerate", str(FPS), "-t", f"{secs + 0.5:.2f}", "-i", str(path))

    def bar(self, text, secs):
        name = "bar-" + hashlib.sha1(repr(text).encode()).hexdigest()[:10]
        path = self.out / f"{name}.png"
        if not path.exists():
            card(path, bar_body(text), BAR)
        return self.image(path, secs)

    def add(self, f):
        self.filters.append(f)


def render(cast, gif):
    if not gif.exists():
        run("agg", "--idle-time-limit", "100000", "--fps-cap", str(FPS), "--font-size", "14", str(cast), str(gif))
    return gif


def main(out):
    out = Path(out).resolve()
    marks = []
    for line in (out / "marks.tsv").read_text().splitlines():
        t, m = line.split("\t", 1)
        marks.append((float(t), m.strip()))
    at = {m: t for t, m in reversed(marks)}  # each mark's first time
    t0, tb, tt = at["start"], at["browser"], at["terminal"]
    marks = [x for x in marks if not tb < x[0] < tt]  # the browser stretch is one piece

    term = render(out / "term.cast", out / "term.gif")
    workers = [l.split("\t") for l in (out / "workers.tsv").read_text().splitlines() if l]
    panes = []  # (label, gif, the cast's wall-clock start)
    for label, _ in workers:
        cast = out / f"pane-{label}.cast"
        if cast.exists():
            panes.append((label, render(cast, out / f"pane-{label}.gif"), json.loads(cast.open().readline())["start"]))
    frames = [l.split("\t") for l in (out / "browser" / "frames.tsv").read_text().splitlines() if l]
    b0 = float(frames[0][1])
    lst = []
    for (f, t), (_, t2) in zip(frames, frames[1:] + [(None, float(frames[-1][1]) + 1)]):
        lst += [f"file '{out / 'browser' / f}'", f"duration {float(t2) - float(t):.3f}"]
    lst.append(f"file '{out / 'browser' / frames[-1][0]}'")
    (out / "browser" / "list.txt").write_text("\n".join(lst) + "\n")
    run("ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", str(out / "browser" / "list.txt"),
        "-vf", f"fps={FPS},pad=ceil(iw/2)*2:ceil(ih/2)*2", "-pix_fmt", "yuv420p", str(out / "browser.mp4"))

    # Stretches: (source, start, end in wall-clock seconds, speed, caption).
    stretches, beat = [], 1
    for (t, m), (t2, _) in zip(marks, marks[1:]):
        budget, b = STRETCH.get(m, (3, None))
        beat = b or beat
        src = "browser" if m == "browser" else "panes" if m == "tiled" and panes else "term"
        if t2 - t < 0.05:
            continue
        speed = max(1.0, (t2 - t) / budget)
        text = CAPTIONS[beat] + (f"   ⏩ {speed:.0f}×" if speed >= 2 else "")
        stretches.append((src, t, t2, speed, text))

    g = Graph(out)
    tin, bin_ = g.input("-i", str(term)), g.input("-i", str(out / "browser.mp4"))
    pin = {label: g.input("-i", str(gif)) for label, gif, _ in panes}
    uses = {"term": sum(s[0] == "term" for s in stretches) + 1}
    for label in pin:
        uses[label] = sum(s[0] == "panes" for s in stretches)
    g.add(f"[{tin}]split={uses['term']}" + "".join(f"[term{i}]" for i in range(uses["term"])))
    for label, n in uses.items():
        if label != "term" and n > 1:
            g.add(f"[{pin[label]}]split={n}" + "".join(f"[{label}{i}]" for i in range(n)))
    used = dict.fromkeys(uses, 0)

    def take(label):
        i = used[label]
        used[label] += 1
        return f"{label}{i}" if label == "term" or uses[label] > 1 else pin[label]

    def retime(pad, a, z, speed):
        return f"[{pad}]trim={a:.2f}:{z:.2f},setpts=(PTS-STARTPTS)/{speed:.3f}"

    groups, cur = [], []
    title = card(out / "title.png", f"<div style='font-weight:700;font-size:64px;color:#f5c542'>🐝 {TITLE[0]}</div>"
                 f"<div style='font-weight:500;font-size:30px;margin-top:14px'>{html.escape(TITLE[1])}</div>",
                 H + BAR)
    g.add(f"[{g.image(title, TITLE_LEN)}]trim=duration={TITLE_LEN},setpts=PTS-STARTPTS,{fit(W, H + BAR)}[title]")
    cur.append(("[title]", TITLE_LEN))
    for i, (src, t, t2, speed, text) in enumerate(stretches):
        secs = (t2 - t) / speed
        if src == "term":
            g.add(retime(take("term"), t - t0, t2 - t0, speed) + f",{fit()}[v{i}]")
        elif src == "browser":
            g.add(retime(bin_, max(0, t - b0), tt - b0, speed) + f",{fit()}[v{i}]")
        else:  # the three workers, stacked, each under its label
            rows, h = [], H // len(panes) - ROW_BAR
            for label, _, start in panes:
                lab = out / f"label-{label}.png"
                if not lab.exists():
                    card(lab, f"<div style='font:600 17px Menlo,monospace;color:#f5c542'>{html.escape(label)}</div>", ROW_BAR)
                lab = g.image(lab, secs)
                g.add(retime(take(label), max(0, t - start), t2 - start, speed) + f",{fit(W, h)}[r{i}{label}]")
                g.add(f"[{lab}][r{i}{label}]vstack=shortest=1[rr{i}{label}]")
                rows.append(f"[rr{i}{label}]")
            g.add(f"{''.join(rows)}vstack=inputs={len(rows)},{fit()}[v{i}]")
        g.add(f"[v{i}][{g.bar(text, secs)}]vstack=shortest=1[p{i}]")
        piece = (f"[p{i}]", secs)
        if src == "browser":
            groups += [cur, [piece]]
            cur = []
        else:
            cur.append(piece)
    # The end card: the last terminal frame, held, over the summary and the repo.
    end = stretches[-1][2] - t0
    g.add(f"[{take('term')}]trim=start={end - 0.3:.2f},setpts=PTS-STARTPTS,"
          f"tpad=stop_mode=clone:stop_duration={TAIL},trim=duration={TAIL},{fit()}[vend]")
    g.add(f"[vend][{g.bar(END, TAIL)}]vstack=shortest=1[pend]")
    cur.append(("[pend]", TAIL))
    groups.append(cur)

    # Concatenate within each group, then wipe from one group into the next.
    for k, parts in enumerate(groups):
        g.add(f"{''.join(p for p, _ in parts)}concat=n={len(parts)}:v=1:a=0[g{k}]")
    acc, prev = sum(d for _, d in groups[0]), "g0"
    for k in range(1, len(groups)):
        wipe = "wipeleft" if k % 2 else "wiperight"
        g.add(f"[{prev}][g{k}]xfade=transition={wipe}:duration={WIPE}:offset={acc - WIPE:.2f}[x{k}]")
        acc += sum(d for _, d in groups[k]) - WIPE
        prev = f"x{k}"
    mp4, gif = out / "demo.mp4", out / "demo.gif"
    run("ffmpeg", "-y", *g.inputs, "-filter_complex", ";".join(g.filters), "-map", f"[{prev}]",
        "-t", f"{acc:.2f}", "-r", str(FPS), "-pix_fmt", "yuv420p", "-c:v", "libx264", "-crf", "20", str(mp4))
    run("gifski", "--fps", str(FPS), "--width", str(W), "--quality", "75", "-o", str(gif), str(mp4))
    (out / "edit.tsv").write_text("".join(f"{s}\t{a - t0:.1f}\t{z - t0:.1f}\t{v:.1f}\t{x}\n"
                                          for s, a, z, v, x in stretches))
    print(f"{mp4}: {acc:.1f}s; {gif}: {gif.stat().st_size / 1e6:.1f} MB; {len(stretches)} stretches")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: edit.py <out dir>")
    main(sys.argv[1])
