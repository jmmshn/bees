`--goal pr`: the work is one or more changes (pull requests, CLs) the worker uploads to the review host from its workspace, complete once every one is merged. `--goal pr:ready` is complete once handed off (open, not a draft, required CI green); `--goal pr:draft` once a change exists, even a draft, with no CI check at all; it may close unmerged.
<!-- worker -->

- Upload your work as a change (the review host's section says how; a draft under `--goal pr:draft`), then hand it off: `{{bs}} done --pr <url>`. Upload further commits to that change whenever the work needs them, then `{{bs}} done --pr <url>` again.
- bs checks each change you name: open and not a draft, holding a commit made in this workspace, uploaded (the host shows your latest commit), current with its base, and every required CI check green; checks nothing requires never count, and none required is a vacuous pass bs says aloud (`pr:draft`: a draft may do, open or not; it need not be current, and CI isn't checked). It refuses (exit 7) naming each failure, and exit 6 when it couldn't ask the review host: run it again.
- Only pending left (required CI still running, or the host not yet showing your latest upload) is exit 10: run `{{bs}} done --wait --pr <url>`. bs then waits itself, up to 8 minutes, so let that call run that long rather than cutting it short; it hands off when CI is green or names what failed (exit 7), and still pending then is exit 11: run it again. Never hand off and walk away from a red or pending change.
- A stack, or a follow-up change: name each one with `--pr`; the changes you handed off before stay handed off. A change that replaces an earlier one: `{{bs}} done --pr <new> --replaces <old>`.
- Never merge or approve a change yourself: landing it is the queen's.
<!-- queen -->

- A `--goal pr` worker's `{{bs}} done` names its changes; `{{bs}} check <worker>` runs the same checks again before you merge. Merge when the keeper says so, then `{{bs}} end <worker>`: it ends the worker once every change it handed off is merged, and refuses (exit 9) naming each one that isn't.
- `pr:draft`: `{{bs}} prune` also removes a workspace whose change was closed unmerged.
