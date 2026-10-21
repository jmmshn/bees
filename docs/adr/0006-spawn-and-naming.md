# Spawn the crew way: one name per bead, project as scope, herdr behind a port

> Amended by [ADR-0008](0008-config-files-one-tagged-struct.md): the worktree parent and the repo lookup come from config, and a `--repo` that is a linked worktree is the base, not the location.

A bee's name is a function of its bead. Bead ids are Jira-style, `<project>-<n>` with children `<project>-<n>.<m>` (`bs epic` assigns the next top-level number), and the name is the id with `.` turned into `-` (`bees-12.3` → `bees-12-3`). Project names are lowercased with `-` and `_` dropped and match `^[a-z][a-z0-9]{0,24}$` (R27⁶), so a name's first `-` always ends the project, and `bd query`'s id wildcard (a SQL LIKE, where `_` matches any character) selects exactly one project's beads.

That one string is used for everything:

- the Claude session name;
- the herdr agent name;
- the git branch;
- the worktree directory `<repo>-WT/<name>`;
- the message address.

The herdr workspace label is 1–4 words the queen writes from the bead (R41, bees-1.20); it was the bead id before. Nothing finds a bee by its label. A name too long for herdr (32 characters) is refused rather than hashed. In v1 there is one bee per bead.

**Project is the scope, as in crew's `--project`.** It is resolved from `--project`, then `BS_PROJECT`, then the basename of git's common directory. Using the common directory means a worktree resolves to its main repo. Each project gets:

- its own logbook, `~/.local/share/bees/<project>.db`;
- its repo, `$BS_REPOS/<project>`;
- a queen named `queen-<project>`.

The project goes into the queen's name because Claude's and herdr's names are machine-wide; bee names already carry it through the bead id. A bead belongs to the project when its id matches `^<project>-\d+(\.\d+)*$`, unless `--repo` overrides.

**Spawn steps, in order:**

1. `herdr worktree create`.
2. Write a `spawned` entry (subject = name; data = bead, workspace, worktree, branch) straight away.
3. Store the brief as a message.
4. `herdr agent start` with `--name <name> --settings '{"crossSessionInbound":"accept"}'` and the doorbell as Claude's first prompt.

No settings file is written: in a worktree Claude reads `.claude/settings.local.json` from the main checkout, so per-bee settings must ride `--settings`. No `BS_*` variables are set either: the bee's `bs` finds its name by walking up to its Claude process, and its project from the worktree's git common directory.

Any failure after step 2 is healed by running spawn again, which resumes in the recorded workspace with `claude --continue`, so the bee keeps its conversation. Delivering the first assignment as the first prompt avoids racing Claude's socket registration, so spawn doesn't depend on ringing. `bs end` removes only the workspace id recorded in `spawned`, then writes `ended`; it keeps the branch, and `bs prune` deletes it once merged (R42).

## Considered Options

- **A separate roster table.** Rejected: the name derives from the bead, herdr and git enforce uniqueness, and the only fact nothing upstream keeps is the workspace id. That fits in one entry, and the lookup key, `subject`, is already a column.
- **Branch slugs from bead titles and crew's hash fallback.** Rejected: four spellings of one identity. Titles change; readability comes from `bd show`.
- **Guessing the workspace from herdr at teardown.** Rejected: crew nearly tore down its first mate's workspace that way.
- **One untyped `Run(argv)` seam for herdr and bd.** Rejected in favour of typed `Herdr` and `Beads` ports, each with a production adapter and a stateful fake, so tests assert behaviour rather than command-line arguments.

## Consequences

- **Orphans:** a workspace open on the bee's worktree `<repo>-WT/<name>` but with no `spawned` entry (a crash between steps 1 and 2) is reported and never removed automatically.
- **Code:** two packages, `internal/project` (resolution and identity) and `internal/bee` (`Fleet.Spawn`, `Fleet.End`, the ports and their CLI adapters).
