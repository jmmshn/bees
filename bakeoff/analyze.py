"""analyze.py [runs dir]: per-run table from transcripts + metrics.json, as markdown (and runs.json next to the runs).

Keeper turns = the lead's typed prompts in its transcript (not keeper.log). Times are from transcripts, so the
keeper's own reaction delay is excluded: t_delegate = t0 -> first worker activity; t_ready = t0 -> the lead's
last message before the keeper's first merge line (its ready report).
"""
import json, pathlib, subprocess, sys
from datetime import datetime

here = pathlib.Path(__file__).parent
runs = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else pathlib.Path.home() / ".cache/bees-bakeoff/runs")
ts = lambda s: datetime.fromisoformat(s.replace("Z", "+00:00"))
rows = []
for d in sorted(p for p in runs.iterdir() if (p / "metrics.json").exists()):
    m = json.loads((d / "metrics.json").read_text())
    tl = json.loads(subprocess.run([sys.executable, "-I", str(here / "timeline.py"), str(d), "--json"], capture_output=True, text=True).stdout or "[]")
    # Typed keeper lines (timeline.py tells them from peer messages and task notifications).
    cap = [r for r in tl if r["who"] == "lead" and r["kind"] == "in"]
    # A line typed while the lead was busy is logged twice (queued, then delivered): keep the first.
    cap = [r for i, r in enumerate(cap) if not (i and r["text"] == cap[i - 1]["text"] and (ts(r["ts"]) - ts(cap[i - 1]["ts"])).total_seconds() < 60)]
    menus = [r for r in tl if r["who"] == "lead" and r["kind"] == "tool:AskUserQuestion"]  # answered by the keeper in a menu
    workers = [r for r in tl if r["who"] != "lead"]
    t0 = ts(cap[0]["ts"]) if cap else None
    t_del = round((ts(workers[0]["ts"]) - t0).total_seconds()) if cap and workers else None
    # Ready = the lead's last message before the keeper's first merge line (or before its second line when nothing merges).
    rep = None
    stop = next((r for r in cap if "merge" in r["text"].lower()), cap[1] if len(cap) > 1 else None)
    if cap:
        says = [r for r in tl if r["who"] == "lead" and r["kind"] == "say" and (not stop or r["ts"] < stop["ts"])]
        rep = round((ts(says[-1]["ts"]) - t0).total_seconds()) if says else None
    left = m["leftovers"]
    rows.append(dict(id=m["id"], script=m["script"], arm=m["arm"], pass_=m["check"]["pass"], turns=len(cap) + len(menus),
                     inter=m["interventions"], delegated=bool(workers), t_delegate=t_del, t_ready=rep,
                     workers=len({r["who"] for r in workers}), out_tok=m["tokens"]["output"],
                     ctx_tok=m["tokens"]["cache_read"] + m["tokens"]["cache_write"] + m["tokens"]["input"],
                     leftovers=sum(len(v) if isinstance(v, list) else v for v in left.values()),
                     remote_left=len(left["remote_branches"])))
(runs / "runs.json").write_text(json.dumps(rows, indent=1) + "\n")
print("| Run | Script | Arm | Pass | Keeper turns | Interventions | Delegated | t delegate (s) | t ready (s) | Workers | Output tok | Context tok (M) | Leftovers (remote) |")
print("|---|---|---|---|---|---|---|---|---|---|---|---|---|")
for r in rows:
    print(f"| {r['id']} | {r['script']} | {r['arm'].upper()} | {'✅' if r['pass_'] else '❌'} | {r['turns']} | {r['inter']} | {'yes' if r['delegated'] else '**no**'} | {r['t_delegate'] if r['t_delegate'] is not None else '–'} | {r['t_ready'] if r['t_ready'] is not None else '–'} | {r['workers']} | {r['out_tok']} | {r['ctx_tok']/1e6:.2f} | {r['leftovers']} ({r['remote_left']}) |")
