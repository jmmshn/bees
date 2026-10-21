Workers have no worktree and no branch of their own: each opens in the same directory, which other workers may share and which may not be a git checkout at all.
<!-- worker -->

- Do the work in this directory and leave your files here. Never switch branches, make a worktree or another checkout, or `git init`; commit only if your bead asks. Leave alone files you didn't make or change for your bead.
- Your done report lists the files you made or changed.
<!-- queen -->

- `bs spawn` opens each worker in its own herdr workspace on the same directory, so workers share it: hand out beads that touch different files, or one at a time.
- `bs end <worker>` closes its workspace and touches no file. There are no branches to prune.
- A worker asks to `git init`, make a worktree, or commit on a branch → no: leaving its files in the directory is what it delivers.
