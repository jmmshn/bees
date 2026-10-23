`--goal push=<url>`: the work is commits added to someone else's open change (the keeper's, an outside contributor's), from the worker's own branch, cut at that change's head. Complete once the change merges; `push:ready=` and `push:draft=` once handed off.
<!-- worker -->

- Commit on your branch, then push it onto the change's head with the exact command your brief gives (a bare `git push` publishes nothing: your branch's upstream is the change's head, under another name), never a force-push. Then `{{bs}} done`: bs checks that the change is open, its head is your branch's tip with a commit made since you were spawned, current with its base, and every required CI check is green (`push:draft=`: only the head). Only pending left is exit 10: `{{bs}} done --wait` waits for it.
<!-- queen -->

- `--goal push=<url>` needs an open change of the project's repo, or a fork's that lets the project push to it, and one no open spawn owns: a fix to a worker's own change is that worker's, so spawn is refused naming it (`next: {{bs}} spawn … <its bead>`). `{{bs}} end <worker>` waits for that change to merge (exit 9 until then).
