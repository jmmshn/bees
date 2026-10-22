`--goal push=<pr-url>`: the work is commits pushed onto someone else's open PR; the worker's worktree starts detached at that PR's head and owns no branch. Complete once the PR merges; `push:ready=` and `push:draft=` once handed off.
<!-- worker -->

- Commit on top of the PR's head, and push onto its branch with the exact command your brief gives (`git push <remote> HEAD:<branch>`), never a force-push. Then `bs done`: bs checks that the PR's head now descends from a commit made here, is current with its base, and CI is green (`push:draft=`: the first and the last). CI still running is exit 10: `bs done --wait` waits for it.
<!-- queen -->

- `--goal push=<url>` needs an open PR of the project's repo; a fork's PR only when it lets maintainers edit it. `bs end <worker>` waits for that PR to merge (exit 9 until then).
