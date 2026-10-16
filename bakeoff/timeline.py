"""timeline.py <run dir> [--json]: one chronological list of what every session in a run did, from its copied transcripts.

Rows: in (captain lines), peer (bs doorbells, cross-session, subagent hand-backs), notify (task notifications), assistant text, tool calls (Bash commands, Agent/SendMessage), per session.
"""
import json, pathlib, re, sys

run = pathlib.Path(sys.argv[1])


def inkind(text, origin=None):
    """in = typed by the captain; peer = another session (bs doorbell, cross-session, subagent hand-back); notify = task notification."""
    t = text.lstrip()
    if origin == "task-notification" or t.startswith("<task-notification>"):
        return "notify"
    if origin == "peer" or t.startswith(("<cross-session-message", "<agent-message", "[bs] ")):
        return "peer"
    return "in"
rows = []
for f in sorted((run / "transcripts").rglob("*.jsonl")):
    who = f.stem[:8] if "subagents" not in f.parts else "sub:" + f.stem.replace("agent-", "")[:8]
    tdir = next(p for p in f.parents if p.parent.name == "transcripts").name
    where = tdir.split("-WT-")[-1] if "-WT-" in tdir else ("lead" if "subagents" not in f.parts else who)
    for line in f.open():
        try: e = json.loads(line)
        except ValueError: continue
        ts, m, a = e.get("timestamp"), e.get("message"), e.get("attachment")
        if ts and isinstance(a, dict) and a.get("type") == "queued_command":  # typed or delivered while the session was busy
            rows.append((ts, where, inkind(str(a.get("prompt")), (a.get("origin") or {}).get("kind")), a.get("prompt")))
        if not ts or not isinstance(m, dict): continue
        c = m.get("content")
        parts = [{"type": "text", "text": c}] if isinstance(c, str) else (c or [])
        for p in parts:
            t = p.get("type")
            if m.get("role") == "user" and t == "text" and not e.get("isMeta"):
                rows.append((ts, where, inkind(p["text"], (e.get("origin") or {}).get("kind")), p["text"]))
            elif m.get("role") == "assistant" and t == "text" and p["text"].strip():
                rows.append((ts, where, "say", p["text"]))
            elif t == "tool_use":
                i = p.get("input", {})
                arg = i.get("command") or i.get("prompt") or i.get("message") or i.get("description") or json.dumps(i)[:200]
                rows.append((ts, where, "tool:" + p["name"], arg))
# Committed timelines must not carry anyone's email (a worker once set its commit identity from the captain's).
rows = sorted((a, b, c, re.sub(r"[\w.+-]+@[\w-]+\.[\w.]+", "<email>", str(d))) for a, b, c, d in rows)
if "--json" in sys.argv:
    print(json.dumps([dict(ts=a, who=b, kind=c, text=d) for a, b, c, d in rows]))
else:
    for ts, who, kind, text in rows:
        print(f"{ts[11:19]} {who:>14} {kind:<12} {' '.join(str(text).split())[:160]}".rstrip())
