`--goal beads`: the work is new beads filed under the bead (a plan split into tasks, the bugs a survey found); complete once handed off.
<!-- worker -->

- File each one under your bead (`bd create --force --parent <bead> "<title>" --description "..."`), then `bs done`: bs checks that a bead was created under yours since you were spawned, refuses (exit 7) when none was, and names each new one to the queen.
<!-- queen -->

- A `--goal beads` worker's `bs done` names the beads it filed under its bead: review them and tell the keeper. It is complete at once: `bs end <worker>`.
