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
# The stretch starting at each mark: its budget in output seconds, its beat (None: the beat
# it is in), and optionally a caption of its own. The browser stretch runs from "browser"
# to "terminal"; "tiled" to "untiled" shows the three workers' panes stacked.
STRETCH = {
    "start": (1, 1),
    "beat1": (7, 1),  # the question typed; the queen files beads and spawns
    "spawned": (5, 1, "1 · each worker gets its own branch and worktree"),
    "branches": (0.5, 1),
    "tiled": (8, 1, "1 · three Claude workers prototype in parallel: straight, zigzag, loop"),
    "untiled": (1.5, 1),
    "studied": (1, 1),
    "beat2": (5, 2),  # the queen's git logs; plannotator starts
    "browser": (14, 2),  # the review
    "terminal": (5, 3),  # the queen picks, sends the build, ends two
    "beat3": (6, 3),  # the steer
    "steered": (1, 3),
    "wait": (3, None),
    "built": (2, 3),
    "beat4": (6, 4),  # merge, graph
    "flight": (6, 4),  # the bee flies, zoomed
    "flew": (3, 4),  # branch -D the two studies
    "pruned": (2, 4),  # the queen's close-out
    "closed": (2, 4),
    "finale": (5, 4),  # the graph and the bee again, zoomed
}
# Stretches shown zoomed: a second agg render at ZOOM_FONT, cropped to the keeper pane's top
# rows (herdr's sidebar takes the first PANE_COL columns), so the bee is big enough to see.
ZOOMED, ZOOM_FONT, PANE_COL, ZOOM_COLS, ZOOM_ROWS = {"flight", "finale"}, 28, 26, 84, 17


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
    text, _, badge = text.partition("   ⏩")  # the speed-up sits at the right edge, so the
    badge = f"<div style='position:absolute;right:18px;font:600 20px sans-serif;color:#8b949e'>⏩{html.escape(badge)}</div>" if badge else ""
    return f"{badge}<div style='font-weight:600;font-size:25px'>{html.escape(text)}</div>"  # caption stays put across a wipe


def fit(w=W, h=H):
    """Scale a stream to fit w×h and pad it onto a canvas of that size."""
    return (f"scale={w}:{h}:force_original_aspect_ratio=decrease:flags=lanczos,"
            f"pad={w}:{h}:(ow-iw)/2:(oh-ih)/2:color={BG},fps={FPS},format=yuv420p,setsar=1")


def render(cast, gif, font=14):
    """The cast as a GIF at real time, by agg."""
    if not gif.exists():
        run("agg", "--idle-time-limit", "100000", "--fps-cap", str(FPS), "--font-size", str(font), str(cast), str(gif))
    return gif


def dims(path):
    r = subprocess.run(["ffprobe", "-v", "error", "-show_entries", "stream=width,height", "-of", "csv=p=0", str(path)],
                       capture_output=True, text=True, check=True)
    return tuple(int(x) for x in r.stdout.split(",")[:2])


def probe(path):
    """A video file's duration in seconds."""
    r = subprocess.run(["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", str(path)],
                       capture_output=True, text=True, check=True)
    return float(r.stdout)


ENC = ["-r", str(FPS), "-pix_fmt", "yuv420p", "-c:v", "libx264", "-crf", "18", "-preset", "veryfast"]


def cfr(src, dst):
    """src as a constant-frame-rate video: agg writes a frame only when the screen changes,
    and a cut inside an idle stretch must still find frames."""
    if not dst.exists():
        run("ffmpeg", "-y", "-i", str(src), "-vf", f"fps={FPS},pad=ceil(iw/2)*2:ceil(ih/2)*2", *ENC, str(dst))
    return dst


def still(path, secs):
    """ffmpeg input args for a still image held secs."""
    return ["-loop", "1", "-framerate", str(FPS), "-t", f"{secs:.3f}", "-i", str(path)]


def clip(path, a, z):
    """ffmpeg input args for path from a to z seconds."""
    return ["-ss", f"{max(0.0, a):.3f}", "-t", f"{z - max(0.0, a):.3f}", "-i", str(path)]


def piece(dst, inputs, graph, secs):
    """Render one piece, secs long, and check it came out that long."""
    run("ffmpeg", "-y", *inputs, "-filter_complex", graph, "-map", "[out]", "-t", f"{secs:.3f}", *ENC, str(dst))
    got = probe(dst)
    if abs(got - secs) > 0.25:
        sys.exit(f"{dst.name}: {got:.2f}s, planned {secs:.2f}s")
    return dst


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
    # CDP can deliver a frame a hair before the one it follows: order them by their own time.
    frames = sorted((l.split("\t") for l in (out / "browser" / "frames.tsv").read_text().splitlines() if l),
                    key=lambda f: float(f[1]))
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
        budget, b, *own = STRETCH.get(m, (3, None))
        beat = b or beat
        src = ("browser" if m == "browser" else "panes" if m == "tiled" and panes
               else "zoom" if m in ZOOMED else "term")
        if t2 - t < 0.05:
            continue
        speed = max(1.0, (t2 - t) / budget)
        text = (own[0] if own else CAPTIONS[beat]) + (f"   ⏩ {speed:.0f}×" if speed >= 2 else "")
        stretches.append((src, t, t2, speed, text))

    def bar(text):
        path = out / ("bar-" + hashlib.sha1(repr(text).encode()).hexdigest()[:10] + ".png")
        return path if path.exists() else card(path, bar_body(text), BAR)

    term_v = cfr(term, out / "term.mp4")
    if any(st[0] == "zoom" for st in stretches):
        size = json.loads((out / "term.cast").open().readline())["term"]
        zoom_v = cfr(render(out / "term.cast", out / "term-zoom.gif", ZOOM_FONT), out / "term-zoom.mp4")
        zw, zh = dims(zoom_v)
        cw, ch = zw / size["cols"], zh / size["rows"]
        crop = f"crop={ZOOM_COLS * cw:.0f}:{ZOOM_ROWS * ch:.0f}:{PANE_COL * cw:.0f}:{ch:.0f}"
    pane_v = {label: (cfr(gif, out / f"pane-{label}.mp4"), start) for label, gif, start in panes}
    parts = out / "parts"
    parts.mkdir(exist_ok=True)
    title = card(out / "title.png", f"<div style='font-weight:700;font-size:64px;color:#f5c542'>🐝 {TITLE[0]}</div>"
                 f"<div style='font-weight:500;font-size:30px;margin-top:14px'>{html.escape(TITLE[1])}</div>",
                 H + BAR)
    groups = [[piece(parts / "00-title.mp4", still(title, TITLE_LEN), f"[0:v]{fit(W, H + BAR)}[out]", TITLE_LEN)]]
    for i, (src, t, t2, speed, text) in enumerate(stretches, 1):
        secs = (t2 - t) / speed
        dst = parts / f"{i:02d}-{src}.mp4"
        retime = f"setpts=(PTS-STARTPTS)/{speed:.4f}"
        if src == "panes":  # the three workers, stacked, each under its label
            ins, h = [], H // len(panes) - ROW_BAR
            for k, (label, (video, start)) in enumerate(pane_v.items()):
                lab = out / f"label-{label}.png"
                if not lab.exists():
                    card(lab, f"<div style='font:600 17px Menlo,monospace;color:#f5c542'>{html.escape(label)}</div>", ROW_BAR)
                ins += clip(video, t - start, t2 - start) + still(lab, secs)
            n = len(pane_v)
            graph = ";".join(f"[{2 * k}:v]{retime},{fit(W, h)}[c{k}];[{2 * k + 1}:v][c{k}]vstack[r{k}]" for k in range(n))
            graph += ";" + "".join(f"[r{k}]" for k in range(n)) + f"vstack=inputs={n},{fit()}[v]"
            ins += still(bar(text), secs)
            graph += f";[v][{2 * n}:v]vstack[out]"
        else:
            if src == "browser":
                video, base, z = out / "browser.mp4", b0, tt
            else:
                video, base, z = zoom_v if src == "zoom" else term_v, t0, t2
            ins = clip(video, t - base, z - base) + still(bar(text), secs)
            graph = f"[0:v]{retime},{crop + ',' if src == 'zoom' else ''}{fit()}[v];[v][1:v]vstack[out]"
        part = piece(dst, ins, graph, secs)
        if src == "browser":
            groups += [[part], []]
        else:
            groups[-1].append(part)
    # The end card: the last frame, held, over the summary and the repo.
    end, last = stretches[-1][2] - t0, stretches[-1][0]
    groups[-1].append(piece(parts / "99-end.mp4", clip(zoom_v if last == "zoom" else term_v, end - 0.5, end)
                            + still(bar(END), TAIL),
                            f"[0:v]tpad=stop_mode=clone:stop_duration={TAIL},trim=duration={TAIL},setpts=PTS-STARTPTS,"
                            f"{crop + ',' if last == 'zoom' else ''}{fit()}[v];[v][1:v]vstack[out]", TAIL))

    # Join each group's pieces, then wipe from one group into the next.
    joined = []
    for k, grp in enumerate(groups):
        lst = parts / f"group{k}.txt"
        lst.write_text("".join(f"file '{p}'\n" for p in grp))
        dst = parts / f"group{k}.mp4"
        run("ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", str(lst), "-c", "copy", str(dst))
        joined.append(dst)
    mp4, gif = out / "demo.mp4", out / "demo.gif"
    acc, prev = probe(joined[0]), joined[0]
    for k, nxt in enumerate(joined[1:], 1):
        wipe = "wipeleft" if k % 2 else "wiperight"
        dst = mp4 if k == len(joined) - 1 else parts / f"wiped{k}.mp4"
        run("ffmpeg", "-y", "-i", str(prev), "-i", str(nxt), "-filter_complex",
            f"[0:v][1:v]xfade=transition={wipe}:duration={WIPE}:offset={acc - WIPE:.3f}[out]",
            "-map", "[out]", *ENC, str(dst))
        acc, prev = probe(dst), dst
    run("gifski", "--fps", str(FPS), "--width", str(W), "--quality", "65", "-o", str(gif), str(mp4))
    (out / "edit.tsv").write_text("".join(f"{s}\t{a - t0:.1f}\t{z - t0:.1f}\t{v:.1f}\t{x}\n"
                                          for s, a, z, v, x in stretches))
    print(f"{mp4}: {probe(mp4):.1f}s; {gif}: {gif.stat().st_size / 1e6:.1f} MB; {len(stretches)} stretches")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: edit.py <out dir>")
    main(sys.argv[1])
