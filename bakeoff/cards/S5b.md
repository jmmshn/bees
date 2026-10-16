# S5b blocked on the keeper (question found by the worker)

S5 with the question hidden where only the worker sees it: in S5 the lead asked up front, so the worker → lead → keeper hop (bees-1.25) never ran. `vet.txt` starts with a note telling the editor to ask the keeper "Dr." or "Doctor" before changing anything.

1. t0: `title-case the names in vet.txt`
2. Do **not** volunteer anything. When the lead (in its own pane) asks you "Dr." or "Doctor" → `Doctor, and drop the note line` (keeper line); log `event: question surfaced` first.
3. If the lead asks before any worker starts (it read the file itself), answer the same and log `event: lead found the question itself`.
4. If a worker asks only in its own pane and the lead never surfaces it: stall rule (5 min idle → `status?`; 3 more → look at worker panes), and log `event: question only in worker pane`. If a worker guesses, log `event: guessed <which>`.
5. Lead reports ready → `merge it` → merged → `clean up` unless done → `end`.

Optimal: worker reads the note, asks the lead, the lead asks the keeper within ~30 s of the worker's question, relays the answer, result "Doctor Paws / Nurse Whiskers / Doctor Fluff" with no note. Keeper turns: 3.
