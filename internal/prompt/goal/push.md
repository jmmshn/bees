`--goal push=<url>`: the work is commits added to someone else's open change; the worker's workspace starts at that change's head and owns no branch. Complete once the change merges; `push:ready=` and `push:draft=` once handed off.
<!-- worker -->

- Commit on top of the change's head, and upload onto it with the exact command your brief gives, never a force-push. Then `{{bs}} done`: bs checks that the change now holds a commit made here, is current with its base, and every required CI check is green (`push:draft=`: only the first). Only pending left is exit 10: `{{bs}} done --wait` waits for it.
<!-- queen -->

- `--goal push=<url>` needs an open change of the project's repo; one whose head lives elsewhere (a fork) only when it lets the project push to it. `{{bs}} end <worker>` waits for that change to merge (exit 9 until then).
