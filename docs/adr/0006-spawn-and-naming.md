# Spawn the crew way: one name per bead, project as scope, herdr behind a port

A peon's name is a function of its bead: `peon-` plus the bead id without `beads-`, with `.` turned into `-` (`beads-995.8` → `peon-995-8`).

That one string is used for everything:
- the Claude session name;
- the herdr agent name;
- the git branch;
- the worktree directory `<repo>-WT/<name>`;
- the message address.

The herdr workspace label is the full bead id. A name too long for herdr (32 characters) is refused rather than hashed. In v1 there is one peon per bead.

**Project is the scope, as in crew's `--project`.** It is resolved from `--project`, then `PNS_PROJECT`, then the basename of git's common directory. Using the common directory means a worktree resolves to its main repo. Each project gets:
- its own logbook, `~/.local/share/peons/<project>.db`;
- its repo, `$PNS_REPOS/<project>`;
- a chief named `chief-<project>`.

The project goes into the chief's name because Claude's and herdr's names are machine-wide. Peon names don't need it, because bead ids are already global. A bead's repo label (its own or its nearest ancestor's) must equal the project, unless `--repo` overrides.

**Spawn steps, in order:**
1. `herdr worktree create`.
2. Write a `spawned` entry (subject = name; data = bead, workspace, worktree, branch) straight away.
3. Store the brief as a message.
4. `herdr agent start` with `--name <name> --settings '{"crossSessionInbound":"accept"}'` and the doorbell as Claude's first prompt.

No settings file is written: in a worktree Claude reads `.claude/settings.local.json` from the main checkout, so per-peon settings must ride `--settings`. No `PNS_*` variables are set either: the peon's `pns` finds its name by walking up to its Claude process, and its project from the worktree's git common directory.

Any failure after step 2 is healed by running spawn again, which resumes in the recorded workspace. Delivering the first assignment as the first prompt avoids racing Claude's socket registration, so spawn doesn't depend on ringing. `pns end` removes only the workspace id recorded in `spawned`, deletes the branch if it is fully merged (`git branch -d`), then writes `ended`.

## Considered Options

- **A separate roster table.** Rejected: the name derives from the bead, herdr and git enforce uniqueness, and the only fact nothing upstream keeps is the workspace id. That fits in one entry, and the lookup key, `subject`, is already a column.
- **Branch slugs from bead titles and crew's hash fallback.** Rejected: four spellings of one identity. Titles change; readability comes from `bd show`.
- **Guessing the workspace from herdr at teardown.** Rejected: crew nearly tore down its first mate's workspace that way.
- **One untyped `Run(argv)` seam for herdr and bd.** Rejected in favour of typed `Herdr` and `Beads` ports, each with a production adapter and a stateful fake, so tests assert behaviour rather than command-line arguments.

## Consequences

- **Orphans:** a workspace labelled with a bead but with no `spawned` entry (a crash between steps 1 and 2) is reported and never removed automatically.
- **Code:** two packages, `internal/project` (resolution and identity) and `internal/peon` (`Fleet.Spawn`, `Fleet.End`, the ports and their CLI adapters).
