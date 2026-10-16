"""tokens.py <transcript dir>...: token totals over every Claude transcript (incl. subagents/) under the dirs, as JSON."""
import json, pathlib, sys
tot = {"sessions": 0, "subagents": 0, "input": 0, "output": 0, "cache_read": 0, "cache_write": 0}
seen = set()
for d in sys.argv[1:]:
    for f in pathlib.Path(d).rglob("*.jsonl"):
        tot["subagents" if "subagents" in f.parts else "sessions"] += 1
        for line in f.open():
            try: e = json.loads(line)
            except ValueError: continue
            m = e.get("message") if isinstance(e, dict) else None
            if not isinstance(m, dict) or "usage" not in m: continue
            key = (m.get("id"), e.get("requestId"))
            if key in seen: continue
            seen.add(key)
            u = m["usage"]
            tot["input"] += u.get("input_tokens", 0); tot["output"] += u.get("output_tokens", 0)
            tot["cache_read"] += u.get("cache_read_input_tokens", 0); tot["cache_write"] += u.get("cache_creation_input_tokens", 0)
print(json.dumps(tot))
