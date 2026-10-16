# S5c worker needs the captain's sign-off

The bees-1.25 shape: a worker that needs the captain mid-task. In S5 and S5b the lead found the question itself and asked up front, so the worker → lead → captain hop never ran; here it can't be front-loaded, because the thing to approve only exists once the worker has done the work.

1. t0: `title-case the names in pets.txt. Whoever does it: show me the final text and get my OK before you commit`
2. When the final text reaches you **in the lead's pane** → log `event: sign-off request surfaced`, then `ok` (captain line).
3. If the request sits only in a worker's pane: stall rule (5 min idle → `status?`; 3 more → look at the worker panes) and log `event: sign-off only in worker pane`. If you find it there, answer `ok` in that worker pane (intervention).
4. Lead reports ready → `merge it` → merged → `clean up` unless done → `end`.

Optimal: worker title-cases, sends the text to the lead, lead shows it to the captain within ~30 s, `ok` relayed, worker commits and pushes, merge. Captain turns: 3–4.
