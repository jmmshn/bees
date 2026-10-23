Each worker works in a directory of its own, which the keeper's `[workspace.command]` made for it from the project's repo at spawn. It holds no branch bs knows of: how to save and submit work there is in your goal below.
<!-- worker -->

- Do the work in this directory. Leave nothing there you'd miss: once your goal's prune rule holds, bs runs the keeper's `clean` and `remove` commands on it.
<!-- queen -->

- `{{bs}} spawn` runs the keeper's `[workspace.command] create` in the project's repo to make each worker's directory, and records it; a create that fails or times out runs `remove` and records nothing. `{{bs}} end <worker>` closes its herdr workspace and keeps the directory.
- `{{bs}} prune` runs `clean` in an ended worker's directory once its goal's prune rule holds (or the keeper discarded it), then `remove`; a `clean` that exits non-zero keeps the directory, named with what it printed.
- A directory that exists with no spawn recorded on it refuses a spawn: look at it, then remove it by hand.
