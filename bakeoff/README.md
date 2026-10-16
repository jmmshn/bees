# bakeoff

Scripted interaction runs that compare bees against Claude Code alone on toy problems (bees-1.33). The results are written up in [docs/bakeoff.md](../docs/bakeoff.md).

Each **script** (`cards/S*.md`, S1–S17) is a keeper's card: the exact lines a human types to a lead session, what triggers each line, and the optimal play. Each script is played by three **arms**:

| Arm | Lead | Workers |
|---|---|---|
| a | `claude` + [arms/A-subagents.md](arms/A-subagents.md) | background Agent subagents, `isolation: worktree` |
| b | `claude` + [arms/B-peers.md](arms/B-peers.md) | separate `claude` sessions in herdr worktrees, cross-session messages |
| c | `bs queen` | `bs spawn` workers, bs logbook |

## Run one

```sh
bakeoff/mkrun.sh c 3                       # fresh toy repo + bare origin + lead in a herdr workspace -> runs/t3c
# play cards/S3.md by hand, or tell a keeper agent: "read cards/DRIVER.md, then play cards/S3.md against <run dir>"
bakeoff/observe.sh ~/.cache/bees-bakeoff/runs/t3c   # snapshot: lead, worker panes, branches
bakeoff/collect.sh ~/.cache/bees-bakeoff/runs/t3c   # metrics.json: wall time, keeper turns, interventions, tokens, leftovers, check
```

- **Location of runs.** Runs live in `$BAKEOFF_RUNS`, which defaults to `~/.cache/bees-bakeoff/runs`. That directory is outside any repo on purpose: Claude walks up parent directories for `CLAUDE.md`, so a toy repo placed inside bees would load bees' dogfood rules into every lead.
- **Which bs.** Arm c runs the `bs` on PATH. To replay a branch's prompts without installing it (only the queen installs bs), build it and pass its path: `go build -o bin/bs ./cmd/bs && BAKEOFF_BS=bin/bs bakeoff/mkrun.sh c 1`. The queen inherits that PATH, so its spawns use the same build.
- **Project names.** A toy repo's directory is `t<script><arm>`, and that is its bs project name, so bees runs never touch the real `bees` logbook.
- **Toy repo setup.**
  - `fixtures/toy/` is the starting tree. It includes an `AGENTS.md` that says to push the branch and report the sha instead of opening a PR, and a `.claude/settings.json` that sets auto mode and accepts cross-session messages.
  - `fixtures/golden/` holds the expected results.
  - `check.sh` grades a run against them.
- **Setup steps the harness does for the keeper.**
  - Each new toy repo gets `commit.gpgsign false`.
  - `mkrun.sh` answers Claude's folder-trust prompt once.
