# S4b conflict (adjacent)


S4 with colours that both sort right after `silver`, so the second merge must conflict (in S4, amber and teal landed at opposite ends and git merged them cleanly).
Tests a merge conflict between two workers on the same file.

1. t0: `two separate tasks, one worker each: add tan to colors.txt, and add teal to colors.txt. Keep the file sorted.`
2. Lead reports both ready (or one) → `merge the tan one first`.
3. Merged → `now merge the teal one`.
4. Expect a conflict. Let the lead handle it; log `event:` for who resolved it (lead itself, the worker, or you) and how.
5. Both merged → `clean up` unless done → `end`.

Optimal: second merge conflicts or is detected as stale → lead has the teal worker merge main and resolve → lead re-checks (sorted, no markers) → merge → cleanup. Captain turns: 3–4, interventions: only merge `!` lines.
