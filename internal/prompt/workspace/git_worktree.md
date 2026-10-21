Each worker works in its own git worktree, on its own branch named after it.
<!-- worker -->

- Do the work in this worktree, on this branch. Your branch is yours: commit to it and push it whenever the work needs it, with no approval. Never check out another branch here, push any other branch, force-push, or merge your branch into another.
- Your done report names your branch and its last commit.
<!-- queen -->

- `bs spawn` cuts each worker its own worktree and branch. `bs end <worker>` removes the worktree its spawn recorded and keeps its branch.
- Now and then run `bs prune`: it deletes ended workers' local branches once their work is on main (rebase and squash merges included), never touches remote branches, and says which it kept and why. Report only what its output says.
- `git branch -d` says unmerged after a rebase or squash merge → the work is on main under other SHAs → use `bs prune`.
