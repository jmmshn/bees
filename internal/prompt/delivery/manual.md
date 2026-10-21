The keeper reviews a worker's branch and lands its work by hand, and the keeper closes its bead.
<!-- worker -->

- Done: commit your work on your own branch, `bs send queen-{{project}} "done: <bead>"` with the branch name and its last commit, then stop.
- Leave `bd close` to the keeper: they close your bead once your work has landed.
- Your branch is yours: commit to it and push it whenever the work needs it, with no approval. Never push to the default branch or any other branch, never force-push, never merge your branch into another.
<!-- queen -->

- A worker reports `done: <bead>`; that means "ready for the keeper", and the bead stays open. Tell the keeper the branch and commit it reported. Landing the work and `bd close` are the keeper's; once they say it has landed, `bs end` the worker.
