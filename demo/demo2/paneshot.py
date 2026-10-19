#!/usr/bin/env python3
"""paneshot.py <out dir> <label>=<pane> ...: record herdr panes as asciinema casts.

A herdr client shows one workspace at a time, so the three workers can't share one cast.
This polls each pane's visible screen (herdr pane read --source visible --format ansi) once
a second, keeps its last ROWS lines, and writes <out>/pane-<label>.cast, one full redraw
per change. Event times are seconds since the cast header's wall-clock "start" (a float,
for edit.py). Runs until SIGTERM. HERDR_SOCKET_PATH must point at the session.
"""

import json
import re
import signal
import subprocess
import sys
import time
from pathlib import Path

ROWS = 14
ANSI = re.compile(r"\x1b\[[0-9;?]*[A-Za-z]")


def snap(pane):
    r = subprocess.run(["herdr", "pane", "read", pane, "--source", "visible", "--format", "ansi"],
                       capture_output=True, text=True)
    if r.returncode:
        return None
    lines = r.stdout.rstrip("\n").split("\n")
    while lines and not ANSI.sub("", lines[-1]).strip():
        lines.pop()
    return lines[-ROWS:]


def main(out, specs):
    out = Path(out)
    panes = dict(s.split("=", 1) for s in specs)
    start = time.time()
    first = {label: snap(p) or [] for label, p in panes.items()}
    width = max([len(ANSI.sub("", l)) for ls in first.values() for l in ls] + [80])
    files, last = {}, {}
    for label in panes:
        f = open(out / f"pane-{label}.cast", "w")
        f.write(json.dumps({"version": 2, "width": min(width, 160), "height": ROWS, "start": start}) + "\n")
        files[label] = f
    stop = []
    signal.signal(signal.SIGTERM, lambda *_: stop.append(1))
    while not stop:
        for label, pane in panes.items():
            lines = snap(pane)
            if lines is None or lines == last.get(label):
                continue
            last[label] = lines
            frame = "\x1b[H\x1b[2J\x1b[0m" + "\r\n".join(l + "\x1b[0m" for l in lines)
            files[label].write(json.dumps([round(time.time() - start, 3), "o", frame]) + "\n")
            files[label].flush()
        time.sleep(1)
    for f in files.values():
        f.close()


if __name__ == "__main__":
    if len(sys.argv) < 3:
        sys.exit("usage: paneshot.py <out dir> <label>=<pane> ...")
    main(sys.argv[1], sys.argv[2:])
