# Keeper lines

The lines replayed in the first take (bees-8.1), one `record.sh say` each. Line 2 named the
branch (`bsdemo-3-1`); here it says "the worker's branch". Wait for the queen to go idle before
the next line.

1. `wc.py needs a --top N flag that prints the N most common words with their counts. File it as a bead under a new epic and spawn a worker for it, label "top words". The worker should commit on its branch and report done with the branch name; there is no remote, so no PR.`

   The queen runs `bs epic` and `bs bead new`, then `bs spawn --label "top words"`. The worker
   implements, tests, commits and runs `bs send … "done: … branch …"`. The doorbell rings the
   queen, which relays the branch. Then `record.sh look`, and `record.sh merge` as the keeper.

2. `Merged the worker's branch into main. Show me bs status, then close the bead and end the worker.`

   The queen runs `bs status`, `bd close`, then `bs end`.

3. `Close the epic and run bs prune.`

   `bs prune` deletes the merged branch.
