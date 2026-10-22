Each worker works in its own git worktree, on its own branch named after it, cut from the branch the project's checkout had out at spawn (its base).
<!-- worker -->

- Do the work in this worktree, on this branch. Your branch is yours: commit to it whenever the work needs it, with no approval. Never check out another branch here, force-push, rewrite another branch, or merge your branch into another.
- Commit everything you hand off: bs reads your work from your branch's commits, and a file left uncommitted or untracked stops `{{bs}} done`.
<!-- queen -->

- `{{bs}} spawn` cuts each worker its own worktree and branch through herdr. `{{bs}} end <worker>` closes its herdr workspace and keeps the worktree and branch.
- `{{bs}} prune` removes an ended worker's worktree once its goal's prune rule holds and the worktree has no uncommitted or untracked files, then deletes its local branch once that is merged into its base (rebase and squash merges included) or the keeper discarded the goal. It never touches a remote branch, and says when one is left: remote branches are the keeper's.
- `git branch -d` says unmerged after a rebase or squash merge → the work is on the base under other commits → use `{{bs}} prune`.
