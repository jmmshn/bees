`--goal branch`: the work is the worker's branch, merged into its base by the queen. `--goal branch:ready` is complete once handed off; prune still waits for the merge.
<!-- worker -->

- Your goal is your branch: commit your work to it, leave nothing uncommitted or untracked, then run `{{bs}} done`. bs checks that your branch has commits its base lacks and that your worktree is clean, and refuses (exit 7) naming each failure.
- Never merge your branch yourself: landing it is the queen's.
<!-- queen -->

- A `--goal branch` worker's `{{bs}} done` means its branch is ready to merge. Review it (`git log <base>..<worker>`, `git diff <base>...<worker>`); when the keeper says to land it, merge it into its base in the project's checkout yourself (`git merge --squash <worker>` and a commit, or `git merge <worker>`), then `{{bs}} end <worker>`: it ends the worker once the branch is merged, by ancestry or, after a squash or rebase, by its changed files being identical on the base.
