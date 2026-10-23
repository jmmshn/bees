`--goal pr`: the work is one change (a pull request, a CL) the worker uploads to the review host from its own branch, complete once it is merged. `--goal pr:ready` is complete once handed off (open, not a draft, required CI green); `--goal pr:draft` once a change exists, even a draft, with no CI check at all; it may close unmerged.
<!-- worker -->

- Upload your work as a change (the review host's section says how; a draft under `--goal pr:draft`), then hand it off: `{{bs}} done`. bs finds the change itself, from your branch: you name none. Upload further commits to that change whenever the work needs them, then `{{bs}} done` again.
- Keep one open change from your branch: two open is refused (exit 7) naming both, and none says how to open one.
- bs checks that change: open and not a draft, its head your branch's tip with a commit made since you were spawned, uploaded (the host shows your latest commit), current with its base, and every required CI check green; checks nothing requires never count, a required one not reported yet is pending, and a base that requires none is a vacuous pass bs says aloud (`pr:draft`: a draft may do, open or not; it need not be current, and CI isn't checked). It refuses (exit 7) naming each failure, and exit 6 when it couldn't ask the review host: run it again.
- Only pending left (required CI still running, or the host not yet showing your latest upload) is exit 10: run `{{bs}} done --wait`. bs then waits itself, up to 8 minutes, so let that call run that long rather than cutting it short; it hands off when CI is green or names what failed (exit 7), and still pending then is exit 11: run it again. Never hand off and walk away from a red or pending change.
- A change that replaces your open one: close the old one first.
- Never merge or approve a change yourself: landing it is the queen's.
<!-- queen -->

- A `--goal pr` worker's `{{bs}} done` names the change bs found from its branch; `{{bs}} check <worker>` runs the same checks again before you merge. Merge when the keeper says so, then `{{bs}} end <worker>`: it ends the worker once that change is merged, and refuses (exit 9) naming it while it isn't.
- `pr:draft`: `{{bs}} prune` also removes a workspace whose change was closed unmerged.
