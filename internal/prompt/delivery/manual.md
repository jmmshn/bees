The keeper reviews a worker's work and lands it by hand, and the keeper closes its bead.
<!-- worker -->

- Done: `bs send queen-{{project}} "done: <bead>"` with what `## Workspace` says your done report names, then stop.
- Leave `bd close` to the keeper: they close your bead once your work has landed.
<!-- queen -->

- A worker reports `done: <bead>`; that means "ready for the keeper", and the bead stays open. Tell the keeper what it reported (its branch and commit, or its files). Landing the work and `bd close` are the keeper's; once they say it has landed, `bs end` the worker.
