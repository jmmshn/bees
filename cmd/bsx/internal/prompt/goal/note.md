`--goal note`: the work is written into the bead itself, its notes or design; complete once handed off.
<!-- worker -->

- Write your findings into your bead: `bd update <bead> --append-notes "<text>"`, which keeps the notes already there. Never leave them only in a file in your workspace or a scratch dir: nobody reads it, and it goes when the workspace does.
- Then `bs done`: bs checks that your bead's notes or design differ from when you were spawned, and refuses (exit 7) when they don't. A label or status change doesn't count.
<!-- queen -->

- A `--goal note` worker's `bs done` means its findings are in its bead: `bd show <bead>`, and relay them to the keeper. It is complete at once: `bs end <worker>`.
