# Setting up bs

Linux or macOS. Needs Go, [Claude Code](https://code.claude.com) with cross-session messaging (2.1.296 or later), [herdr](https://herdr.dev) for spawning workers, and `bd` 1.1.0 or later for beads, with one local store shared by every repo (`BEADS_DIR`; the skill below sets it up).

For a guided setup, run the [`setup-bees`](../.claude/skills/setup-bees/SKILL.md) skill in Claude Code from this repo (`/setup-bees`): it checks and installs each piece, then runs a queen → worker round trip.

## Install

```sh
go install ./cmd/bs     # from main in this repo; in a bees fleet, only the queen or keeper does this
bs config init          # optional: a commented ~/.config/bees/config.toml, every key at its default
bs doctor               # ok / WARN / FAIL per check, run from a project's repo
```

bs reads `~/.config/bees/config.toml` and `projects/<project>.toml` beside it ([ADR-0010](adr/0010-core-rebuild.md), [ADR-0008](adr/0008-config-files-one-tagged-struct.md)); with no files it runs on the defaults, its logbooks in `~/.local/share/bees/<project>.db`. On a `herdr --session <session>` server it reads `~/.config/bees/sessions/<session>/` instead, refuses every logbook command while that dir is missing, and never uses the real logbooks' dir: that is how a live test or a second fleet runs beside the keeper's. [`SETUP-AGENT.md`](SETUP-AGENT.md) sets one up step by step.

```sh
bs config                          # every value and where it came from
bs doctor                          # config, project, logbook dir, harness and ring-socket modes, herdr server vs client,
                                   # herdr socket, bd store, gh and a GitHub remote, cmd argv, stray BS_*
bs status                          # one line per spawn (GOAL STATE HANDOFF COMPLETE RUNNING), then stuck/kept/waived/discarded/late
bs send --project <other> queen-<other> "..."   # cross-project mail; the reply hint carries --project back
```

`bs doctor` runs even when the config or the project can't be worked out: that is its first FAIL, the checks that need no project still run, and it prints a `skip` line for each it couldn't make. Only `git_worktree` workspaces work before bees-13.10 and 13.11: doctor fails any other kind and `config init` won't write one. `bs status` reads the logbook, the process table and the disk, and asks herdr only to heal an unconfirmed start, so with herdr down that start reads `unknown` and the rest still prints.

The first bs call on a logbook written by an older bs migrates it to schema 2, copying it aside to `<project>.db.pre-2` first. bs refuses a logbook where a session the old bs started is still open (it can neither ring nor see it run), naming each: end them first (`bs end <worker>` and `bs status` still run, warning).

## Names

- **Project:** the basename of the git repo you are in (a worktree counts as its main repo), lowercased with `-` and `_` dropped (`pymatgen-analysis-defects` → `pymatgenanalysisdefects`). Inside a recorded herdr pane, the project is the logbook that recorded the pane. Outside any repo, set `BS_PROJECT`.
- **Queen:** `queen-<project>`.
- **Bead ids:** `<project>-<n>` for epics (`bs epic "<title>"` makes the next one), `<project>-<n>.<m>` for their children (`bs bead new --parent <epic> "<title>"`). `bs bead ls` lists only this project's beads; the queen and workers are told to use these instead of `bd list`/`bd create`.
- **Worker:** the bead id with `.` → `-` (`bees-2.1` → `bees-2-1`). That one name is its harness session, herdr agent, git branch, worktree directory (`<repo>-WT/<name>`) and address for `bs send`.

## Run a queen and workers

```sh
cd ~/repos/<you>/<project>
bs queen                       # records this pane's queen, then execs the harness; restart her this way
# inside the queen:
bs epic "ringing"              # → <project>-2
bs bead new --parent <project>-2 --description "…" "…"   # → <project>-2.1
bs bead ls                     # this project's beads that aren't closed
bs spawn --goal pr --label "ring on send" <project>-2.1  # worker <project>-2-1 in its own herdr workspace and worktree
bs send <project>-2-1 "…"      # store, then ring it awake; --now interrupts its current turn
bs inbox                       # your unread messages
bs status                      # each worker's goal, handoff, completion, whether it runs, and what is stuck
bs check <project>-2-1         # run its handoff checks again, before merging
bs end <project>-2-1           # the completion gate: ends it once its goal is complete (exit 9 names what isn't)
bs prune                       # remove ended workers' workspaces once their goal's prune rule holds
# inside a worker:
bs done --pr <url>             # hand off: bs checks the goal and tells the queen (exit 7 names each failure)
```

`--goal` is required and fixed at spawn: `pr` (complete when merged), `pr:ready`, `pr:draft`, `push=<url>`, `review=<url>`, `verdict`, `note`, `beads`, `branch` ([ADR-0010](adr/0010-core-rebuild.md#goals)). `bs goal <worker> <goal>` amends a live one. The queen's merge turn is: land it, `bs end`, `bs prune`, `bd close`.

After a change to bs merges, rebuild it from `main` with `go install ./cmd/bs`; in a bees fleet that is the queen's job. Workers never install bs: they build into their worktree (`go build -o bin/bs ./cmd/bs`) and run `./bin/bs`. Inside this repo, bs warns when the shared bs was built from a commit behind `main` or not on it (a branch build), and when a worktree's `bin/bs` doesn't match its checkout; a worker is told to ask the queen rather than reinstall. `bs version` prints the commit it was built from.

## Folder trust

Claude asks whether to trust a repo the first time it opens one, and its worktrees share the answer. Open the repo in Claude once (`bs queen` does) before spawning. If a worker is spawned into an untrusted repo, `bs spawn` warns and the worker waits at the trust prompt in its herdr pane until you answer it.

## Worker permissions

`bs spawn` starts each worker with its own `--settings` JSON, the only per-session place Claude's auto mode classifier reads `autoMode` from (it ignores the project's `.claude/settings*.json`). Each `autoMode` list keeps `"$defaults"`, so the built-in rules (force push, exfiltration) stay. `permissions.deny` blocks, before the classifier, what `[guards]` resolves for the workspace: under `git_worktree`, pushes to the default branch, force-push, and the review host's merge and approve; `[guards] deny` adds globs, and `allow_merge_into` lifts the merge deny only while the host protects the default branch. Bash rules match commands as written, not every spelling.

**Protect `main` on GitHub** (Settings → Branches: require a pull request). The deny rules are a speed bump that a shell can spell around; branch protection is the guard that keeps a worker's push off `main`.

No sandbox: the keeper chose not to run workers in Claude's bubblewrap sandbox (bees-1.51).

## Outside a pane

bs knows a queen or worker by its recorded herdr pane. From a shell outside one (tests, scripts, the keeper), name the session with `--as`: `bs inbox --as queen-<project>`, `bs end --as queen-<project> <worker>`.

## Check it works

```sh
go vet ./... && go test ./...      # the scenario suite runs every command over fakes and real git
```

Then exercise a change live with the [`live-test`](../.claude/skills/live-test/SKILL.md) skill: a throwaway project on its own `herdr --session` server, torn down with a leak check.

## CI

`.github/workflows/ci.yml` runs two parallel jobs on every pull request and push to `main`. The same checks locally:

```sh
# lint
test -z "$(gofmt -l .)"
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run   # default linters, .golangci.yml
npx markdownlint-cli2@0.23.3                                                 # README, GLOSSARY, AGENTS, docs/
# test
go vet ./... && go test -race ./... && go build ./cmd/bs
```

`.pre-commit-config.yaml` runs the lint job (golangci-lint on changed lines only) on every commit; tests stay in CI. Install the hook once per clone with `uv tool install pre-commit && pre-commit install`; it lands in the shared `.git/hooks`, so every herdr worktree of that clone runs it too (`uvx pre-commit install` works but pins a uv cache path that `uv cache clean` breaks). Check everything with `pre-commit run --all-files`.

## Releases

Tag by hand and push the tag (`git tag v0.1.0 && git push origin v0.1.0`). `.github/workflows/release.yml` runs GoReleaser (`.goreleaser.yaml`): `bs` for linux and darwin, amd64 and arm64, `CGO_ENABLED=0`, a changelog from the commits since the last tag, and `SHA256SUMS`. `bs version` prints the commit from Go's VCS stamp (`-buildvcs=true` fails the build without one). Dry run: `goreleaser release --snapshot --clean`.
