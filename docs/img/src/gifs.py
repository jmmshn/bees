# /// script
# requires-python = ">=3.11"
# dependencies = ["pillow"]
# ///
"""Render the README's side-by-side GIFs (bees-1.48).

    uv run docs/img/src/gifs.py        # writes docs/img/<name>.gif

Each GIF replays one bake-off script (bakeoff/cards, bakeoff/results/observations.md):
left pane is arm A (Claude Code lead + subagents), right pane is arm C (bs).
Every line is copied from that run's transcript; "…" marks a cut. Only the
"#" setup lines and the ✗/✓ verdicts are commentary.
"""

import subprocess
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

OUT = Path(__file__).resolve().parent.parent
COLS, ROWS = 56, 21
SIZE, LINE, PAD, GAP = 14, 19, 14, 12
HOLD = 4.0  # seconds the last frame stays up before the loop restarts

BG, PANE, BAR, RULE = "#0d1117", "#161b22", "#21262d", "#30363d"
STYLE = {
    "note": "#8b949e",  # setup commentary
    "cmd": "#79c0ff",  # $ shell command
    "out": "#c9d1d9",  # command output
    "user": "#ffffff",  # ❯ what the keeper typed
    "claude": "#d2a8ff",  # ● the lead's reply
    "ring": "#e3b341",  # [bs] doorbell
    "err": "#ff7b72",  # a tool error
    "bad": "#ff7b72",  # ✗ verdict
    "good": "#56d364",  # ✓ verdict
}


def font():
    path = subprocess.run(
        ["fc-match", "-f", "%{file}", "DejaVu Sans Mono"], capture_output=True, text=True
    ).stdout
    return ImageFont.truetype(path, SIZE)


# A pane is a title and a script: (seconds after the previous step, style, lines).
S10 = {
    "name": "lead-restart",
    "caption": "bake-off S10, runs t10a / t10c: the lead is killed at commit 2 of 5, a fresh one starts",
    "left": (
        "Claude Code: lead + subagent",
        [
            (0.0, "note", ["# lead killed at commit 2 of 5"]),
            (0.8, "cmd", ["$ claude --name lead-t10a …"]),
            (0.8, "user", ["❯ what's running right now, and how far along is it?"]),
            (
                1.6,
                "claude",
                [
                    "● This session isn't running any workers. The one",
                    "  job in this repo is a separate session called",
                    "  count-500, which I didn't start:",
                    "  - Progress: 200 of 500 lines (40%).",
                ],
            ),
            (2.4, "note", ["# 6 min later"]),
            (0.4, "user", ["❯ status?"]),
            (
                1.2,
                "claude",
                [
                    "● count-500 has stopped at 200/500 and isn't",
                    "  running anymore.",
                ],
            ),
            (
                1.6,
                "bad",
                [
                    "✗ count-500 was another run's session. This run's",
                    "  subagent died with the old lead at 200.",
                ],
            ),
        ],
    ),
    "right": (
        "bees: queen + bee",
        [
            (0.0, "note", ["# queen killed at commit 2 of 5"]),
            (0.8, "cmd", ["$ bs queen"]),
            (0.8, "user", ["❯ what's running right now, and how far along is it?"]),
            (0.8, "cmd", ["$ bs status; echo ---; bs bead ls"]),
            (
                0.4,
                "out",
                [
                    "SESSION     LABEL               STATE    UNREAD",
                    "queen-t10c  -                   running  0",
                    "t10c-1-1    count file commits  running  0",
                    "---",
                    "Found 2 issues:",
                    "○ t10c-1.1 [● P2] [task] - Build count.txt 1..500 …",
                    "○ t10c-1 [● P2] [epic] - count.txt durability test",
                ],
            ),
            (
                1.2,
                "claude",
                [
                    "● One worker is running: t10c-1-1, on t10c-1.1.",
                    "  - Progress: 4 of 5 commits are in on branch …",
                ],
            ),
            (1.6, "ring", ["[bs] 1 unread (t10c-1-1) — run: bs inbox"]),
            (0.6, "cmd", ["$ bs inbox"]),
            (
                0.4,
                "out",
                [
                    "── 001a144a8bd8… from t10c-1-1 · 2026-10-16 05:21:11",
                    "done: t10c-1.1 (count.txt 1..500) branch t10c-1-1",
                    "pushed to origin, final sha 59ab4f5…. Not merged.",
                ],
            ),
            (
                1.2,
                "good",
                [
                    "✓ the bee kept working in its own pane, and its",
                    "  report woke the new queen.",
                ],
            ),
        ],
    ),
}

S12 = {
    "name": "worker-down",
    "caption": "bake-off S12, runs t12a / t12c: the worker is down at commit 2 of 5, the keeper sends it a change",
    "left": (
        "Claude Code: lead + subagent",
        [
            (0.0, "note", ["# subagent stopped at commit 2 of 5"]),
            (
                0.8,
                "user",
                [
                    "❯ message the worker: when you finish, add a final",
                    "  line END after 500",
                ],
            ),
            (0.8, "claude", ["● SendMessage(ac28df3832dae0b12)"]),
            (
                0.4,
                "err",
                [
                    "  └ Agent ac28df3832dae0b12 was stopped by the user",
                    "    and won't be resumed. Treat its work as",
                    "    cancelled; only launch a new agent if the user",
                    "    explicitly asks.",
                ],
            ),
            (
                1.2,
                "claude",
                [
                    "● I couldn't send the message. … Do you want a new",
                    "  worker to pick up from there?",
                ],
            ),
            (1.6, "user", ["❯ now restart the worker and let it finish"]),
            (
                1.2,
                "claude",
                [
                    "● I started a new worker, counter2, in the",
                    "  existing worktree on count-500.",
                ],
            ),
            (
                1.6,
                "bad",
                [
                    "✗ nothing was queued: END reached counter2 only",
                    "  because the lead retyped it into the new brief.",
                ],
            ),
        ],
    ),
    "right": (
        "bees: queen + bee",
        [
            (0.0, "note", ["# bee killed at commit 2 of 5"]),
            (
                0.8,
                "user",
                [
                    "❯ message the worker: when you finish, add a final",
                    "  line END after 500",
                ],
            ),
            (0.8, "cmd", ['$ bs send t12c-1-1 "… add one final line END …"']),
            (
                0.4,
                "out",
                [
                    "sent 001a144b1e1939dd655f292094 to t12c-1-1",
                    "queued; t12c-1-1 is not running",
                ],
            ),
            (1.0, "cmd", ["$ bs spawn t12c-1.1"]),
            (0.4, "out", ["resumed t12c-1-1 (count file commits) in workspace w8T"]),
            (1.0, "note", ["# in the bee t12c-1-1, same conversation"]),
            (0.6, "cmd", ["$ bs inbox"]),
            (
                0.4,
                "out",
                [
                    "── 001a144b1e19… from queen-t12c · 2026-10-16 05:31:10",
                    "Captain's addition to t12c-1.1: after the 5 commits",
                    "are done (lines 1-500), add one final line END after",
                    "500 in count.txt, commit it, push, …",
                ],
            ),
            (
                1.6,
                "good",
                [
                    "✓ the logbook kept the message while the bee was",
                    "  down; it read it first thing on restart.",
                ],
            ),
        ],
    ),
}


def timeline(steps):
    """[(t, style, line)] with absolute times."""
    t, out = 0.0, []
    for dt, style, lines in steps:
        t += dt
        for line in lines:
            assert len(line) <= COLS, f"{len(line)} > {COLS} cols: {line}"
            out.append((t, style, line))
    assert len(out) <= ROWS, f"{len(out)} > {ROWS} rows"
    return out


def render(scene):
    f = font()
    cw = f.getlength("M")
    pw = int(COLS * cw) + 2 * PAD
    bar = LINE + 10
    ph = bar + PAD + ROWS * LINE + PAD
    w, h = 2 * pw + 3 * GAP, ph + 2 * GAP + LINE
    panes = [(title, timeline(steps)) for title, steps in (scene["left"], scene["right"])]
    times = sorted({t for _, tl in panes for t, _, _ in tl})

    def frame(now):
        im = Image.new("RGB", (w, h), BG)
        d = ImageDraw.Draw(im)
        for i, (title, tl) in enumerate(panes):
            x0, y0 = GAP + i * (pw + GAP), GAP
            d.rounded_rectangle([x0, y0, x0 + pw, y0 + ph], 8, fill=PANE, outline=RULE)
            d.rounded_rectangle([x0, y0, x0 + pw, y0 + bar], 8, fill=BAR)
            d.rectangle([x0, y0 + bar - 8, x0 + pw, y0 + bar], fill=BAR)
            dot = STYLE["bad"] if i == 0 else STYLE["good"]
            d.ellipse([x0 + PAD, y0 + bar / 2 - 5, x0 + PAD + 10, y0 + bar / 2 + 5], fill=dot)
            d.text((x0 + PAD + 18, y0 + 5), title, font=f, fill=STYLE["user"])
            shown = [(s, line) for t, s, line in tl if t <= now]
            for row, (s, line) in enumerate(shown):
                d.text((x0 + PAD, y0 + bar + PAD + row * LINE), line, font=f, fill=STYLE[s])
        d.text((GAP, h - GAP - LINE + 2), scene["caption"], font=f, fill=STYLE["note"])
        return im

    frames = [frame(t) for t in times]
    durs = [int((b - a) * 1000) for a, b in zip(times, times[1:])] + [int(HOLD * 1000)]
    pal = frames[-1].quantize(colors=64, method=Image.Quantize.MEDIANCUT)
    frames = [fr.quantize(palette=pal, dither=Image.Dither.NONE) for fr in frames]
    path = OUT / f"{scene['name']}.gif"
    frames[0].save(path, save_all=True, append_images=frames[1:], duration=durs, loop=0, optimize=True)
    print(f"{path.relative_to(OUT.parent.parent)}  {w}x{h}  {len(frames)} frames  {path.stat().st_size // 1024} KB")


if __name__ == "__main__":
    for scene in (S10, S12):
        render(scene)
