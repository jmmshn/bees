`--goal pr`: the work is one or more GitHub PRs from the worker's workspace, complete once every one is merged. `--goal pr:ready` is complete once handed off (open, not a draft, CI green); `--goal pr:draft` once a PR exists, even a draft, and it may close unmerged.
<!-- worker -->

- Push your branch and open a PR from it (`gh pr create`; a draft under `--goal pr:draft`), then hand it off: `bs done --pr <url>`. Push further commits to that PR whenever the work needs them, then `bs done --pr <url>` again.
- bs checks each PR you name: open and not a draft (except `pr:draft`), holding a commit made in this workspace, pushed, current with its base, and CI green. It refuses (exit 7) naming each failure, and exit 6 when it couldn't ask the review host: run it again.
- CI still running is exit 10: run `bs done --wait --pr <url>` (give that shell call a 10-minute timeout). bs waits for CI itself, up to 8 minutes, then hands off when it is green or names what failed (exit 7); still running then is exit 11: run it again. Never hand off and walk away from a red or pending PR.
- A stack, or a follow-up PR: name each one with `--pr`; the PRs you handed off before stay handed off. A PR that replaces an earlier one: `bs done --pr <new> --replaces <old>`.
- Never merge or approve a PR yourself: landing it is the queen's.
<!-- queen -->

- A `--goal pr` worker's `bs done` names its PRs; `bs check <worker>` runs the same checks again before you merge. Merge when the keeper says so, then `bs end <worker>`: it ends the worker once every PR it handed off is merged, and refuses (exit 9) naming each one that isn't.
- `pr:draft`: `bs prune` also removes a workspace whose PR was closed unmerged.
