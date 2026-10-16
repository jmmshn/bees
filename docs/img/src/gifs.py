# /// script
# requires-python = ">=3.11"
# dependencies = ["pillow"]
# ///
"""Render the README's GIFs (bees-1.48).

    uv run docs/img/src/gifs.py        # writes docs/img/<name>.gif

Both GIFs replay bake-off run t2c (bakeoff/cards/S2.md: three jobs in
parallel) as a Claude Code chat in a herdr-style window. The window, the
sidebar and the chat layout are an illustration; the keeper's lines, the
tool calls, their output and the sessions' replies are copied from the
run's transcripts, with "…" marking a cut.
"""

import subprocess
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

OUT = Path(__file__).resolve().parent.parent
SIDE, COLS, ROWS = 20, 72, 22
SIZE, LINE, PAD = 13, 18, 12
HOLD = 4.0  # seconds the last frame stays up before the loop restarts

BG, PANE, BAR, RULE = "#0d1117", "#161b22", "#21262d", "#30363d"
STYLE = {
    "note": "#8b949e",  # caption, idle
    "cmd": "#79c0ff",  # ● Bash(…) tool call
    "out": "#c9d1d9",  # command output
    "user": "#ffffff",  # ❯ what the keeper typed
    "claude": "#d2a8ff",  # ● the session's reply
    "ring": "#e3b341",  # [bs] doorbell, working
    "good": "#56d364",  # done
}
STATE = {"working": "ring", "done": "good", "idle": "note"}


def font():
    path = subprocess.run(
        ["fc-match", "-f", "%{file}", "DejaVu Sans Mono"], capture_output=True, text=True
    ).stdout
    return ImageFont.truetype(path, SIZE)


# A scene is a Claude Code chat, as a script of (seconds after the previous step, kind, payload):
#   kind is a STYLE name: payload is lines appended to the session pane;
#   kind "ws": payload is (label, state) for the sidebar; state None removes it.
def bash(dt, cmd, out, gap=0.4):
    """A Claude Code Bash tool block: the call, then its output under a └."""
    return [
        (dt, "cmd", [f"● Bash({cmd})"]),
        (gap, "out", [("  └ " if i == 0 else "    ") + line for i, line in enumerate(out)]),
    ]


QUEEN = [
    (0.0, "ws", ("queen t2c", "idle")),
    (
        0.0,
        "user",
        [
            "❯ three jobs, in parallel please: sort fruits.txt alphabetically,",
            "  remove duplicate lines from todo.txt, and add a one-line title at",
            "  the top of haiku.txt",
        ],
    ),
    (0.3, "ws", ("queen t2c", "working")),
    *bash(1.2, 'bs epic "toy file cleanups"', ["t2c-1", 'add beads with: bs bead new --parent t2c-1 "..."']),
    *bash(0.8, 'bs bead new --parent t2c-1 "Sort fruits.txt alphabetically" …', ["t2c-1.1", "t2c-1.2", "t2c-1.3"]),
    (0.8, "cmd", ['● Bash(bs spawn --label "sort fruits" t2c-1.1; bs spawn … ; bs status)']),
    (0.6, "out", ["  └ spawned t2c-1-1 (sort fruits) for t2c-1.1 in workspace w6B"]),
    (0.0, "ws", ("sort fruits", "working")),
    (0.6, "out", ["    spawned t2c-1-2 (dedupe todo) for t2c-1.2 in workspace w6C"]),
    (0.0, "ws", ("dedupe todo", "working")),
    (0.6, "out", ["    spawned t2c-1-3 (haiku title) for t2c-1.3 in workspace w6D"]),
    (0.0, "ws", ("haiku title", "working")),
    (
        0.2,
        "out",
        [
            "    SESSION    LABEL        STATE    UNREAD",
            "    queen-t2c  -            running  2",
            "    t2c-1-1    sort fruits  running  0",
            "    …",
        ],
    ),
    (0.0, "ws", ("sort fruits", "done")),
    (0.5, "ws", ("dedupe todo", "done")),
    *bash(
        0.6,
        "bs inbox",
        [
            "── 001a142d6ede… from t2c-1-1 · 2026-10-15 20:52:24",
            "done: t2c-1.1 branch t2c-1-1 sha 972096da… (no PR per AGENTS.md)",
            "── 001a142d7089… from t2c-1-2 · 2026-10-15 20:52:31",
            "done: t2c-1.2 (dedupe todo.txt) branch t2c-1-2 sha 71c0b6d — …",
        ],
    ),
    (0.6, "ws", ("haiku title", "done")),
    (
        0.6,
        "claude",
        ["● Two of the three jobs are done and look right. Reading the third", "  worker's report now."],
    ),
    *bash(
        0.8,
        "bs inbox; git diff main origin/t2c-1-3",
        ["── 001a142d736d… from t2c-1-3 · 2026-10-15 20:52:42", "done: t2c-1.3 (haiku title) branch t2c-1-3 sha e90ba1b — …"],
    ),
    (
        0.8,
        "claude",
        [
            "● All three jobs are done. Each worker pushed its own branch to origin.",
            "  … Say \"merge\" and I'll merge all three with --no-ff and push main,",
            "  then close the beads and end the workers.",
        ],
    ),
    (0.0, "ws", ("queen t2c", "idle")),
    (1.6, "user", ["❯ merge all three"]),
    (0.0, "ws", ("queen t2c", "working")),
    *bash(0.8, "git merge --no-ff … && git push origin main …", ["…", "   6fd050d..727d871  main -> main"]),
    (0.6, "cmd", ["● Bash(bd close t2c-1.1 t2c-1.2 t2c-1.3; bs end t2c-1-1; … ; bs prune)"]),
    (
        0.3,
        "out",
        [
            "  └ ✓ Closed t2c-1.1 — Sort fruits.txt alphabetically: Closed",
            "    ✓ Closed t2c-1.2 — Remove duplicate lines from todo.txt: Closed",
            "    ✓ Closed t2c-1.3 — Add a title line to haiku.txt: Closed",
            "    ended t2c-1-1",
        ],
    ),
    (0.0, "ws", ("sort fruits", None)),
    (0.3, "out", ["    …", "    ended t2c-1-2"]),
    (0.0, "ws", ("dedupe todo", None)),
    (0.3, "out", ["    …", "    ended t2c-1-3"]),
    (0.0, "ws", ("haiku title", None)),
    (0.3, "out", ["    …", "    deleted t2c-1-1: merged, its commits are on origin/main", "    …"]),
    (
        0.8,
        "claude",
        [
            "● I've closed the three beads and t2c-1 (toy file cleanups), ended all",
            "  three workers, and deleted their merged branches.",
        ],
    ),
    (0.0, "ws", ("queen t2c", "idle")),
]

BEE = [
    (0.0, "ws", ("queen t2c", "working")),
    (0.0, "ws", ("sort fruits", "working")),
    (0.0, "ws", ("dedupe todo", "working")),
    (0.0, "ws", ("haiku title", "working")),
    (0.0, "ring", ["❯ [bs] you are t2c-1-1, working on t2c-1.1 — run: bs inbox   …"]),
    *bash(
        1.0,
        "bs inbox; bd show t2c-1.1; ls; git log --oneline -3",
        [
            "── 001a142d69ff… from queen-t2c · 2026-10-15 20:52:04",
            "You own t2c-1.1: Sort fruits.txt alphabetically",
            "",
            "Sort the lines of fruits.txt alphabetically. Touch only fruits.txt.",
            "Commit on your branch, git push origin <branch>, report branch +",
            "sha (no PR; see AGENTS.md).",
            "",
            "○ t2c-1.1 · Sort fruits.txt alphabetically   [● P2 · OPEN]",
            "…",
            "  ↑ ○ t2c-1: (EPIC) toy file cleanups ● P2",
        ],
    ),
    *bash(1.8, "cat -A fruits.txt; sort fruits.txt > /tmp/…/f && cat -A /tmp/…/f", ["pear$", "apple$", "…"]),
    *bash(1.0, "LC_ALL=C sort -o fruits.txt fruits.txt && git add … && git push", ["972096da38412ecb5ca7ab3b5e22fef2bbf3078f"]),
    *bash(0.8, 'bs send queen-t2c "done: t2c-1.1 branch t2c-1-1 sha 972096da… …"', ["sent 001a142d6ede18b179e90982df to queen-t2c"]),
    (0.0, "ws", ("sort fruits", "done")),
    (
        0.8,
        "claude",
        [
            "● I sorted the lines of fruits.txt alphabetically (no other file",
            "  changed), committed it, and pushed branch t2c-1-1 to origin at sha",
            "  972096d. I then sent the queen the done message …",
        ],
    ),
    (0.6, "ws", ("dedupe todo", "done")),
    (0.8, "ws", ("haiku title", "done")),
]

SCENES = [
    ("queen", "queen t2c", "queen-t2c", QUEEN),
    ("bee", "sort fruits", "t2c-1-1", BEE),
]


def render(name, focus, session, script):
    f = font()
    cw = f.getlength("M")
    sw = int(SIDE * cw) + 2 * PAD
    w = sw + int(COLS * cw) + 2 * PAD
    bar = LINE + 8
    h = bar + PAD + ROWS * LINE + PAD
    caption = "illustrated herdr + Claude Code; tool output and replies are real (bake-off run t2c)"
    assert len(caption) * cw <= w - 2 * PAD, caption

    t, events = 0.0, []
    for dt, kind, payload in script:
        t += dt
        events.append((t, kind, payload))
    times = sorted({t for t, _, _ in events})

    def frame(now):
        lines, ws = [], {}
        for t, kind, payload in events:
            if t > now:
                break
            if kind == "ws":
                label, state = payload
                if state is None:
                    ws.pop(label, None)
                else:
                    ws[label] = state
                continue
            for line in payload:
                assert len(line) <= COLS, f"{len(line)} > {COLS} cols: {line}"
                lines.append((kind, line))
        im = Image.new("RGB", (w, h + LINE + PAD), BG)
        d = ImageDraw.Draw(im)
        # sidebar: herdr's workspace list (illustration)
        d.rectangle([0, 0, sw, h], fill=BAR)
        d.text((PAD, 5), "herdr", font=f, fill=STYLE["note"])
        for i, (label, state) in enumerate(ws.items()):
            y = bar + PAD + i * LINE * 1.5
            if label == focus:
                d.rectangle([0, y - 3, sw, y + LINE - 1], fill=RULE)
            d.text((PAD, y), f"{i + 1} {label}", font=f, fill=STYLE["user"])
            d.ellipse([sw - PAD - 8, y + 4, sw - PAD, y + 12], fill=STYLE[STATE[state]])
        # session pane: the last ROWS lines
        d.rectangle([sw, 0, w, bar], fill=PANE)
        d.line([sw, bar, w, bar], fill=RULE)
        d.text((sw + PAD, 5), f"{focus} · claude --name {session}", font=f, fill=STYLE["note"])
        for row, (kind, line) in enumerate(lines[-ROWS:]):
            d.text((sw + PAD, bar + PAD + row * LINE), line, font=f, fill=STYLE[kind])
        d.text((PAD, h + PAD / 2), caption, font=f, fill=STYLE["note"])
        return im

    frames = [frame(t) for t in times]
    durs = [int((b - a) * 1000) for a, b in zip(times, times[1:])] + [int(HOLD * 1000)]
    sheet = Image.new("RGB", (w, frames[0].height * len(frames)))
    for i, fr in enumerate(frames):
        sheet.paste(fr, (0, i * fr.height))
    pal = sheet.quantize(colors=64, method=Image.Quantize.MEDIANCUT)  # one palette for every frame
    frames = [fr.quantize(palette=pal, dither=Image.Dither.NONE) for fr in frames]
    path = OUT / f"{name}.gif"
    frames[0].save(path, save_all=True, append_images=frames[1:], duration=durs, loop=0, optimize=True)
    size = f"{frames[0].width}x{frames[0].height}"
    print(f"{path.relative_to(OUT.parent.parent)}  {size}  {len(frames)} frames  {path.stat().st_size // 1024} KB")


if __name__ == "__main__":
    for scene in SCENES:
        render(*scene)
