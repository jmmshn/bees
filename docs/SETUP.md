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
bs status                          # one line per spawn (LABEL GOAL STATE HANDOFF COMPLETE RUNNING UNREAD), then stuck/kept/waived/discarded/late
bs send --project <other> queen-<other> "..."   # cross-project mail; the reply hint carries --project back
```

`bs doctor` runs even when the config or the project can't be worked out: that is its first FAIL, the checks that need no project still run, and it prints a `skip` line for each it couldn't make. A `none` workspace waits for bees-13.10: doctor fails it and `config init` won't write it; a `command` workspace is a `[workspace.command]` table ([below](#other-vcs-a-command-workspace-and-a-custom-goal)), which doctor fails while it lacks a key. `bs status` reads the logbook, the process table and the disk, and asks herdr only to heal an unconfirmed start, so with herdr down that start reads `unknown` and the rest still prints.

The first bs call on a logbook written by an older bs migrates it to schema 3, copying it aside to `<project>.db.pre-3` first. bs refuses a logbook where a session the old bs started is still open (it can neither ring nor see it run), naming each: end them first (`bs end <worker>` and `bs status` still run, warning).

## Names

- **Project:** the basename of the git repo you are in (a worktree counts as its main repo), lowercased with `-` and `_` dropped (`pymatgen-analysis-defects` → `pymatgenanalysisdefects`). Every call acts in that project, whoever makes it; a queen or worker calling from outside any repo acts in its own. The keeper outside any repo sets `BS_PROJECT`.
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
bs done                        # hand off: bs finds the change from your branch, checks the goal and tells the queen (exit 7 names each failure)
```

`--goal` is required and fixed at spawn: `pr` (complete when merged), `pr:ready`, `pr:draft`, `pr=<url>` (stacked on that change), `push=<url>`, `review=<url>`, `verdict`, `note`, `beads`, `branch` ([ADR-0010](adr/0010-core-rebuild.md#goals)). `bs goal <worker> <goal>` amends a live one. The queen's merge turn is: land it, `bs end`, `bs prune`, `bd close`.

After a change to bs merges, rebuild it from `main` with `go install ./cmd/bs`; in a bees fleet that is the queen's job. Workers never install bs: they build into their worktree (`go build -o bin/bs ./cmd/bs`) and run `./bin/bs`. Inside this repo, bs warns when the shared bs was built from a commit behind `main` or not on it (a branch build), and when a worktree's `bin/bs` doesn't match its checkout; a worker is told to ask the queen rather than reinstall. `bs version` prints the commit it was built from.

## Folder trust

Claude asks whether to trust a repo the first time it opens one, and its worktrees share the answer. Open the repo in Claude once (`bs queen` does) before spawning. If a worker is spawned into an untrusted repo, `bs spawn` warns and the worker waits at the trust prompt in its herdr pane until you answer it.

## Worker permissions

`bs spawn` starts each worker with its own `--settings` JSON, the only per-session place Claude's auto mode classifier reads `autoMode` from (it ignores the project's `.claude/settings*.json`). Each `autoMode` list keeps `"$defaults"`, so the built-in rules (force push, exfiltration) stay. `permissions.deny` blocks, before the classifier, what `[guards]` resolves for the workspace: under `git_worktree`, pushes to the default branch, force-push, and the review host's merge and approve; `[guards] deny` adds globs, and `allow_merge_into` lifts the merge deny only while the host protects the default branch. Bash rules match commands as written, not every spelling.

**Protect `main` on GitHub** (Settings → Branches: require a pull request). The deny rules are a speed bump that a shell can spell around; branch protection is the guard that keeps a worker's push off `main`.

No sandbox: the keeper chose not to run workers in Claude's bubblewrap sandbox (bees-1.51).

## Who bs takes you for

bs acts as its nearest ancestor process a session's entry recorded (a queen, a worker), else as the keeper; `bs status`'s first line says which. From the keeper's shell, `--as <session>` is a debug override that acts as that session, with its permissions: `bs done --as <worker> ...`, `bs send --as queen-<project> <worker> "..."` while her pane is down. `bs inbox --as <session>` only peeks: it marks nothing read.

## Other VCS: a command workspace and a custom goal

git with GitHub needs no config. Any other VCS (jj, hg, a cloud client with CLs) is two tables of your own argv in a config file ([ADR-0010](adr/0010-core-rebuild.md) Workspaces and Goals): `[workspace.command]`, which makes and removes each worker's directory, and a `[goal.<name>]` whose `cmd:` checks say whether the work was handed off and has landed. The queen then spawns with `--goal <name>`.

How bs runs them, the same for both tables:

- **argv, never a shell.** A `{key}` fills one whole argument from the spawn's record: `{dir}` (the worker's directory), `{name}` (the worker), `{bead}`, `{project}`, `{repo}` (the project's directory). A git_worktree also fills `{branch}`, `{base}` and `{start}`; a command workspace has none, and a check naming one is refused at spawn. A worker names nothing, so there is no `{pr}`: a check finds the change from the workspace. Need a pipe? `["sh", "-c", 'script using "$1"', "sh", "{name}"]` passes the value as an argument, never into the script.
- **Where.** `create` and `remove` run in the project's directory (`paths.repo`); `clean` and every `cmd:` check run in the worker's. The environment is only `PATH`, `HOME` and `LANG`, as `bs` has them; `bs spawn` refuses a command whose program isn't on that `PATH`, and `bs doctor` checks each.
- **Bounds.** Each runs within its `timeout` (default 60s); one that runs past it is killed with its process group.
- **Exit 0 is yes, any other exit is no.** A command that can't start or times out is neither: `bs done` exits 6. Write each check so that "nothing found" exits non-zero: `jj log -r <revset>` and `hg log -r <revset>` exit 0 on an empty revset, and `! cmd` turns a network error into yes. Wrap them: `test -n "$(jj log --no-graph -r … -T commit_id)"`.
- **Frozen.** A spawn records each check's argv, `clean`, `remove`, and the sha256 of the goal's `fragment` file: `bs done`, `bs end` and `bs prune` run what was recorded, never the config as it is now. A resume refuses a fragment changed since; `bs goal <worker> <goal>` records the new one.
- **The workspace.** `bs spawn` refuses a `[workspace.command]` with no `clean` (exit 0 when nothing in the workspace would be lost: it is what lets prune remove one), and a `dir` that already exists with no spawn recorded on it. A `create` that fails, times out, or makes no `dir` runs `remove`, and nothing is recorded. `bs end` keeps the directory; `bs prune` runs `clean`, then `remove`, only once the goal's frozen prune rule holds or the keeper discarded it, only on the directory `create` made (not one made there again since), and checks that `remove` left no `dir`. bs looks at nothing inside a command workspace: `clean` is the only guard on what's in it, so make it count ignored files too, where reports end up (an ignored `scratch/` is where five reports were lost, bees-13.7). A second spawn of the same worker waits for the first to record its directory, so two never make or remove it at once. Guards: when the project's directory is a git checkout, a worker is denied pushes to its default branch and force pushes; else only `[guards] deny` applies. `bs spawn` says which. Claude Code would ask to trust each command workspace's directory, a new folder per worker, so a Claude worker's spawn marks that one folder trusted in `~/.claude.json` (`projects[<dir>].hasTrustDialogAccepted`, under Claude's own lock) and prune drops its entry; a `dir` inside a git checkout is left to the repo's trust (bees-13.86).

**The workspace's VCS (optional).** With these four keys set in `[workspace.command]`, all or none, a command workspace takes `--goal pr` like a git worktree: bs asks them what git would tell it, and the review host (`review.host`) finds and reads the change. Each runs in the worker's directory and prints one JSON object on stdout (unknown keys ignored, stderr never read); a non-zero exit, a timeout or anything else printed is "couldn't ask" (`bs done` exits 6), never "no". `{branch}` is the worker's name; `{commit}` and `{ancestor}` are commits the review host or `branch` named.

```text
branch   = [..., "{branch}"]                  → {"tip": "<commit>"}       the workspace's head; absent, "" or null: none
pushed   = [..., "{branch}"]                  → {"tip": "<commit>"}       what it last uploaded; absent, "" or null: can't say
has      = [..., "{commit}"]                  → {"has": true|false}       the workspace has that commit
descends = [..., "{ancestor}", "{commit}"]    → {"descends": true|false}  commit is ancestor or descends from it
```

At spawn bs records `branch`'s tip as where the worker started; `bs done` then checks that the change's head is the workspace's tip and descends from that start. Without the four keys, a goal that reads a branch (`pr`, or a check like `pr_open`) is refused at spawn, naming them. Even with them, what needs git itself is refused: `--goal branch`, `--goal push=<url>` and `--goal pr=<url>` (each cuts or measures a git branch), and the checks `branch_ahead` and `branch_merged`. bs ships no adapter for any tool: the commands are yours.

A custom goal names its checks by stage: `handoff` runs at `bs done`, `complete` at `bs end` (none: complete at handoff), `prune` before `bs prune` removes the workspace (none: at once). Each is a built-in check that needs no branch (`clean`, `verdict_given`, `note_changed`, `bead_created`) or `cmd:<name>` for a `[checks.<name>]`. Its `fragment` is a file of your own (both roles' text, `<!-- worker -->` and `<!-- queen -->` lines splitting it) that becomes the goal's section of each prompt: say there how the worker saves and submits its work.

### jj

Each worker is a jj workspace; it pushes its change to a bookmark named after itself.

```toml
[workspace.command]
create  = ["jj", "workspace", "add", "--name", "{name}", "{dir}"]
remove  = ["sh", "-c", 'jj workspace forget "$1"; rm -rf "$2"', "sh", "{name}", "{dir}"]
clean   = ["sh", "-c", 'test -z "$(jj diff --summary)" && test ! -e scratch']   # an empty working-copy commit; jj never lists ignored files
dir     = "{repo}-WS/{name}"

[checks.jj_pushed]
run = ["sh", "-c", 'test -n "$(jj log --no-graph -r "remote_bookmarks(exact:\"$1\")" -T commit_id)"', "sh", "{name}"]
[checks.jj_landed]
run = ["sh", "-c", 'jj git fetch --quiet && test -n "$(jj log --no-graph -r "remote_bookmarks(exact:\"$1\") & ::trunk()" -T commit_id)"', "sh", "{name}"]
timeout = "2m"

[goal.jj]
handoff  = ["cmd:jj_pushed"]
complete = ["cmd:jj_landed"]
prune    = ["cmd:jj_landed"]
fragment = "~/.config/bees/fragments/jj.md"   # "jj describe, then jj git push --bookmark <your name>"
```

### hg

Each worker is an `hg share` of the repo, its work on a bookmark named after itself. `hg outgoing` exits 1 when nothing is outgoing and 255 when it can't ask, so the check tests for 1, never `! hg outgoing`.

```toml
[workspace.command]
create  = ["hg", "share", "{repo}", "{dir}"]
remove  = ["rm", "-rf", "{dir}"]                                  # a share is its directory
clean   = ["sh", "-c", 'test -z "$(hg status --modified --added --removed --deleted --unknown --ignored)"']
dir     = "{repo}-WS/{name}"

[checks.hg_pushed]
run = ["sh", "-c", 'hg log -r "bookmark(\"$1\")" -T x >/dev/null || exit 1; hg outgoing -q -r "bookmark(\"$1\")" >/dev/null; test $? -eq 1', "sh", "{name}"]
[checks.hg_landed]
run = ["sh", "-c", 'hg pull -q && test -n "$(hg log -r "bookmark(\"$1\") and public()" -T x)"', "sh", "{name}"]

[goal.hg]
handoff  = ["clean", "cmd:hg_pushed"]
complete = ["cmd:hg_landed"]
prune    = ["cmd:hg_landed"]
fragment = "~/.config/bees/fragments/hg.md"
```

### A cloud client and CLs

A client tool that makes a named client per worker and sends its edits as CLs. `cl` below stands for your tool's own commands: each must exit non-zero when the answer is no. The live test runs this shape against a fake `cl`.

```toml
[workspace.command]
create  = ["cl", "client", "new", "{name}", "{dir}"]
remove  = ["cl", "client", "delete", "{name}"]
clean   = ["cl", "client", "clean", "{name}"]   # exit 0: no edit outside a sent CL
dir     = "~/clients/{name}"
timeout = "3m"
# Optional: the client's VCS, so --goal pr works too (one JSON object each, above).
branch   = ["cl", "head", "--json", "{dir}"]
pushed   = ["cl", "uploaded", "--json", "{dir}"]
has      = ["cl", "has", "--json", "{commit}"]
descends = ["cl", "descends", "--json", "{ancestor}", "{commit}"]

[checks.cl_sent]
run = ["cl", "sent", "{name}"]                  # exit 0: the client's CL is out for review
[checks.cl_submitted]
run = ["cl", "submitted", "{name}"]             # exit 0: it is submitted

[goal.cl]
handoff  = ["cmd:cl_sent"]
complete = ["cmd:cl_submitted"]
prune    = ["cmd:cl_submitted"]
fragment = "~/.config/bees/fragments/cl.md"
```

`bs end` is the completion gate (exit 9 until `cl submitted` exits 0); `bs end --incomplete` ends the worker anyway, and prune removes its client once the CL is submitted or the keeper discards the goal.

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
