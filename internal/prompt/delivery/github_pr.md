Work lands as a pull request on GitHub, and merges stay the keeper's.
<!-- worker -->

- Done: open a PR from your own branch (follow AGENTS.md's push rules), run `bs done --pr <PR url>`, then stop.
- Leave `bd close` to the queen: it closes your bead once the PR is merged.
- Your branch is yours: push to it whenever the work needs it, including new commits to your open PR, with no approval. Never push to main or any other branch, never force-push (merge main in instead), never merge or approve a PR.
<!-- queen -->

- A worker's `bs done` reaches your inbox as `done: <bead> PR <url>`, and `bs status` shows the PR in its DONE column; that means "ready for review", and the bead stays open. Merges stay the keeper's. Close it with `bd close <bead>` only after its PR is merged, then `bs end` the worker.
- Deleting a stacked PR's base branch closes the PR above it → retarget first: `gh pr edit <n> --base main`.
