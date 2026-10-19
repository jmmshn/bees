#!/usr/bin/env python3
"""edit.py <out dir>: cut a take (take.sh) into the demo GIF.

Inputs in <out>: term.cast (asciinema, the herdr client), browser/ (review.sh's screencast
frames, each with its wall-clock time) and marks.tsv (wall-clock seconds, then a mark).
Writes term.gif (agg, real time), browser.mp4, demo.mp4, demo.gif and edit.tsv (the cuts).

Each stretch between two marks plays at the speed that fits it into its budget, never
slower than real time; past 2x its caption says so. The browser part wipes in and out.
A caption bar per beat, rendered by headless Chrome, sits under the picture.
"""

import html
import subprocess
import sys
from pathlib import Path

W, H, BAR = 1200, 780, 56  # picture, then the caption bar under it
FPS = 12
WIPE = 0.6
TAIL = 4  # seconds the last frame holds under the end card
CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
BG = "0x0d1117"

CAPTIONS = {
    1: "1 · keeper asks the queen for a design study; she fans it out to 3 workers",
    2: "2 · keeper reviews all three studies in plannotator and picks one",
    3: "3 · the worker that already has the context builds it; keeper steers mid-build",
    4: "4 · plain git merge, and the bee flies",
}
END = "bees: one queen, a fleet of Claude workers, all on local git"
# The stretch starting at each mark: its budget in output seconds, and its beat (None: the
# beat it is in). The browser stretch runs from "browser" to "terminal".
STRETCH = {
    "start": (1.5, 1),
    "beat1": (8, 1),
    "spawned": (8, 1),
    "wait": (5, None),
    "studied": (1, 1),
    "beat2": (5, 2),
    "browser": (15, 2),
    "terminal": (5, 3),
    "beat3": (6, 3),
    "steered": (2, 3),
    "built": (3, 3),
    "beat4": (10, 4),
    "flew": (3, 4),
    "closed": (2, 4),
}


def run(*cmd):
    r = subprocess.run(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
    if r.returncode:
        sys.exit(f"{cmd[0]} failed:\n{r.stderr[-3000:]}")


def caption(out, text, cache={}):
    """A caption bar PNG for text, made once."""
    if text not in cache:
        path = out / f"cap{len(cache)}.png"
        page = path.with_suffix(".html")
        page.write_text(
            f"<html><body style='margin:0;background:#0d1117;height:{BAR}px;display:flex;"
            "align-items:center;justify-content:center;color:#f0f6fc;"
            f"font:600 25px -apple-system,Helvetica,sans-serif'>{html.escape(text)}</body></html>"
        )
        run(CHROME, "--headless=new", "--hide-scrollbars", f"--window-size={W},{BAR}",
            f"--screenshot={path}", page.as_uri())
        cache[text] = path
    return cache[text]


def fit(label):
    """Scale a stream to the picture's width and pad it onto the picture's canvas."""
    return (f"scale={W}:{H}:force_original_aspect_ratio=decrease:flags=lanczos,"
            f"pad={W}:{H}:(ow-iw)/2:(oh-ih)/2:color={BG},fps={FPS},format=yuv420p,setsar=1[{label}]")


def main(out):
    out = Path(out).resolve()
    marks = []
    for line in (out / "marks.tsv").read_text().splitlines():
        t, m = line.split("\t", 1)
        marks.append((float(t), m.strip()))
    t0 = next(t for t, m in marks if m == "start")  # the cast's time 0
    tb = next(t for t, m in marks if m == "browser")
    tt = next(t for t, m in marks if m == "terminal")
    marks = [x for x in marks if not tb < x[0] < tt]  # the browser stretch is one piece

    # Sources: the terminal at real time, the browser frames each held until the next.
    term = out / "term.gif"
    if not term.exists():
        run("agg", "--idle-time-limit", "100000", "--fps-cap", str(FPS), "--font-size", "14",
            str(out / "term.cast"), str(term))
    frames = [l.split("\t") for l in (out / "browser" / "frames.tsv").read_text().splitlines() if l]
    b0 = float(frames[0][1])
    lst = []
    for (f, t), (_, t2) in zip(frames, frames[1:] + [(None, float(frames[-1][1]) + 1)]):
        lst += [f"file '{out / 'browser' / f}'", f"duration {float(t2) - float(t):.3f}"]
    lst.append(f"file '{out / 'browser' / frames[-1][0]}'")
    (out / "browser" / "list.txt").write_text("\n".join(lst) + "\n")
    run("ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", str(out / "browser" / "list.txt"),
        "-vf", f"fps={FPS},pad=ceil(iw/2)*2:ceil(ih/2)*2", "-pix_fmt", "yuv420p", str(out / "browser.mp4"))

    # Stretches: (source, start, end, speed, caption text), times in seconds of the source.
    stretches, beat = [], 1
    for (t, m), (t2, _) in zip(marks, marks[1:]):
        budget, b = STRETCH.get(m, (3, None))
        beat = b or beat
        src, base = ("b", b0) if m == "browser" else ("t", t0)
        a, z = max(0.0, t - base), t2 - base
        if z - a < 0.05:
            continue
        speed = max(1.0, (z - a) / budget)
        text = CAPTIONS[beat] + (f"   ⏩ {speed:.0f}×" if speed >= 2 else "")
        stretches.append((src, a, z, speed, text))

    # Inputs: 0 the terminal, 1 the browser, then one per caption image.
    inputs = ["-i", str(term), "-i", str(out / "browser.mp4")]
    caps = {}

    def cap_input(text):
        if text not in caps:
            caps[text] = 2 + len(caps)
            inputs.extend(["-loop", "1", "-framerate", str(FPS), "-i", str(caption(out, text))])
        return caps[text]

    nt = sum(1 for s in stretches if s[0] == "t")
    filters = [f"[0:v]split={nt + 1}" + "".join(f"[t{i}]" for i in range(nt + 1))]
    groups, cur, lens, ti = [], [], [], 0
    for i, (src, a, z, speed, text) in enumerate(stretches):
        ci = cap_input(text)
        if src == "t":
            pad, ti = f"t{ti}", ti + 1
        else:
            pad = "1:v"
        filters.append(f"[{pad}]trim={a:.2f}:{z:.2f},setpts=(PTS-STARTPTS)/{speed:.3f}," + fit(f"v{i}"))
        filters.append(f"[v{i}][{ci}:v]vstack=shortest=1[p{i}]")
        piece = (f"[p{i}]", (z - a) / speed)
        if src == "b":
            if cur:
                groups.append(cur)
            groups.append([piece])
            cur = []
        else:
            cur.append(piece)
    # The end card: the last terminal frame, held, over the summary line.
    end_t = stretches[-1][2]
    ce = cap_input(END)
    filters.append(f"[t{nt}]trim=start={end_t - 0.3:.2f},setpts=PTS-STARTPTS,"
                   f"tpad=stop_mode=clone:stop_duration={TAIL},trim=duration={TAIL}," + fit("vend"))
    filters.append(f"[vend][{ce}:v]vstack=shortest=1[pend]")
    cur.append(("[pend]", TAIL))
    groups.append(cur)

    # Concatenate within each group, then wipe from one group into the next.
    for g, parts in enumerate(groups):
        filters.append(f"{''.join(p for p, _ in parts)}concat=n={len(parts)}:v=1:a=0[g{g}]")
    acc, prev = sum(d for _, d in groups[0]), "g0"
    for g in range(1, len(groups)):
        wipe = "wipeleft" if g % 2 else "wiperight"
        filters.append(f"[{prev}][g{g}]xfade=transition={wipe}:duration={WIPE}:offset={acc - WIPE:.2f}[x{g}]")
        acc += sum(d for _, d in groups[g]) - WIPE
        prev = f"x{g}"
    mp4, gif = out / "demo.mp4", out / "demo.gif"
    run("ffmpeg", "-y", *inputs, "-filter_complex", ";".join(filters), "-map", f"[{prev}]",
        "-t", f"{acc:.2f}", "-r", str(FPS), "-pix_fmt", "yuv420p", "-c:v", "libx264", "-crf", "20", str(mp4))
    run("gifski", "--fps", str(FPS), "--width", str(W), "--quality", "75", "-o", str(gif), str(mp4))
    (out / "edit.tsv").write_text("".join(f"{s}\t{a:.1f}\t{z:.1f}\t{v:.1f}\t{t}\n" for s, a, z, v, t in stretches))
    print(f"{mp4}: {acc:.1f}s; {gif}: {gif.stat().st_size / 1e6:.1f} MB; {len(stretches)} stretches")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: edit.py <out dir>")
    main(sys.argv[1])
