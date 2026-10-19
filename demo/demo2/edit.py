#!/usr/bin/env python3
"""edit.py <out dir>: cut take.sh's recording into the demo GIF.

Reads <out>/take.mov and <out>/marks.tsv, and writes <out>/demo.mp4 and <out>/demo.gif.
Each stretch between two marks plays at the speed that fits it into its budget (never
slower than real time), so the waits shrink and the typing and clicking stay readable.
A caption bar per beat, rendered by headless Chrome, sits under the picture.
"""

import html
import subprocess
import sys
from pathlib import Path

WIDTH = 1200  # README width
FPS = 12
CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

# The stretch starting at each mark: its budget in output seconds, and the caption shown.
CAPTIONS = {
    1: "1 · keeper asks the queen for a design study; she fans it out to 3 workers",
    2: "2 · keeper reviews all three studies in plannotator and picks one",
    3: "3 · the worker that already has the context builds it; keeper steers mid-build",
    4: "4 · plain git merge, and the bee flies",
    5: "bees: one queen, a fleet of Claude workers, all on local git",
}
STRETCH = {
    "start": (1.5, 1),
    "beat1": (7, 1),  # typing the question, the queen filing and spawning
    "spawned": (10, 1),  # a look at each worker
    "wait": (5, None),  # the caption of the beat it is in
    "studied": (1, 1),
    "beat2": (7, 2),  # the queen's git logs, plannotator starting
    "browser": (16, 2),  # the review: each study, the note, approve
    "approved": (3, 2),  # the slide back
    "terminal": (5, 3),  # the queen's pick, ending two workers
    "beat3": (8, 3),  # the steer
    "steered": (2, 3),
    "built": (4, 3),
    "beat4": (14, 4),  # merge, log, the bee
    "flew": (4, 4),
    "closed": (3, 4),
    "end": (0, 5),
}
TAIL = 4  # seconds the last frame holds, under the summary caption


def run(*cmd):
    subprocess.run(cmd, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


def duration(mov):
    out = subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", str(mov)],
        check=True, capture_output=True, text=True,
    ).stdout
    return float(out)


def caption_png(text, path, height):
    page = path.with_suffix(".html")
    page.write_text(
        "<html><body style='margin:0;background:#0d1117;height:%dpx;display:flex;align-items:center;"
        "justify-content:center;font:600 26px -apple-system,Helvetica,sans-serif;color:#f0f6fc'>"
        "%s</body></html>" % (height, html.escape(text))
    )
    run(CHROME, "--headless=new", "--hide-scrollbars", f"--window-size={WIDTH},{height}",
        f"--screenshot={path}", page.as_uri())


def main(out):
    out = Path(out).resolve()
    mov = out / "take.mov"
    marks = [line.split("\t", 1) for line in (out / "marks.tsv").read_text().splitlines() if line]
    marks = [(float(t), m.strip()) for t, m in marks]
    marks.append((duration(mov), "end"))

    segs, beat = [], 1
    for (t, m), (t2, _) in zip(marks, marks[1:]):
        budget, cap = STRETCH.get(m, (3, None))
        beat = cap or beat
        if t2 - t > 0.05 and budget > 0:
            segs.append((t, t2, max(1.0, (t2 - t) / budget), beat))

    bar = 56
    for n, text in CAPTIONS.items():
        caption_png(text, out / f"cap{n}.png", bar)

    # One trimmed, retimed piece per stretch, each with its caption under it, then concat.
    parts, filters = [], []
    for i, (a, b, speed, cap) in enumerate(segs):
        filters.append(
            f"[0:v]trim={a:.2f}:{b:.2f},setpts=(PTS-STARTPTS)/{speed:.3f},fps={FPS},"
            f"scale={WIDTH}:-2:flags=lanczos[v{i}];[v{i}][{cap}:v]vstack=shortest=1[p{i}]"
        )
        parts.append(f"[p{i}]")
    # The end: the last frame held under the summary caption.
    end = segs[-1][1]
    filters.append(
        f"[0:v]trim=start={end - 0.3:.2f},setpts=PTS-STARTPTS,fps={FPS},scale={WIDTH}:-2:flags=lanczos,"
        f"tpad=stop_mode=clone:stop_duration={TAIL},trim=duration={TAIL}[vt];[vt][5:v]vstack=shortest=1[pt]"
    )
    parts.append("[pt]")
    filters.append(f"{''.join(parts)}concat=n={len(parts)}:v=1:a=0[out]")
    total = sum((b - a) / s for a, b, s, _ in segs) + TAIL
    inputs = ["-i", str(mov)]
    for n in sorted(CAPTIONS):
        inputs += ["-loop", "1", "-framerate", str(FPS), "-i", str(out / f"cap{n}.png")]
    mp4 = out / "demo.mp4"
    run("ffmpeg", "-y", *inputs, "-filter_complex", ";".join(filters), "-map", "[out]",
        "-r", str(FPS), "-pix_fmt", "yuv420p", "-c:v", "libx264", "-crf", "20", str(mp4))
    gif = out / "demo.gif"
    run("gifski", "--fps", str(FPS), "--width", str(WIDTH), "--quality", "80", "-o", str(gif), str(mp4))
    print(f"{mp4} {total:.1f}s; {gif} {gif.stat().st_size / 1e6:.1f} MB; {len(segs)} stretches")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: edit.py <out dir>")
    main(sys.argv[1])
