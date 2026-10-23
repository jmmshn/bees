Each worker works in a directory of its own, which the keeper's `[workspace.command]` made for it from the project's repo at spawn. bs looks at nothing inside it itself. When the keeper set its VCS commands (`branch`, `pushed`, `has`, `descends`), bs asks them for the workspace's head and what it last uploaded, under the worker's name as its branch, and checks your change against them; without them it holds no branch bs knows of. Either way, how to save and submit work there is in your goal below.
<!-- worker -->

- Do the work in this directory. Leave nothing there you'd miss: once your goal's prune rule holds, bs runs the keeper's `clean` and `remove` commands on it.
<!-- queen -->

- `{{bs}} spawn` runs the keeper's `[workspace.command] create` in the project's repo to make each worker's directory, and records it; a create that fails or times out runs `remove` and records nothing. With the VCS commands set, it also records `branch`'s head as where the worker started; without them, a goal that reads a branch (`pr`) is refused, naming them. `{{bs}} end <worker>` closes its herdr workspace and keeps the directory.
- `{{bs}} prune` runs `clean` in an ended worker's directory once its goal's prune rule holds (or the keeper discarded it), then `remove`; a `clean` that exits non-zero keeps the directory, named with what it printed.
- A directory that exists with no spawn recorded on it refuses a spawn: look at it, then remove it by hand.
