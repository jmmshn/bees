# Setting up bs

Linux or macOS. Needs Go, [Claude Code](https://code.claude.com) with cross-session messaging (2.1.296 or later), [herdr](https://herdr.dev) for spawning workers, and `bd` 1.1.0 or later for beads, with one local store shared by every repo (`BEADS_DIR`; the skill below sets it up).

For a guided setup, run the [`setup-bees`](../.claude/skills/setup-bees/SKILL.md) skill in Claude Code from this repo (`/setup-bees`): it checks and installs each piece, then runs a queen → worker round trip.

## Install

```sh
go install ./cmd/bs     # from this repo
bs init                 # creates ~/.local/share/bees/<project>.db (bs queen and bs spawn do too)
```

## Names

- **Project:** the basename of the git repo you are in (a worktree counts as its main repo), lowercased with `-` and `_` dropped (`pymatgen-analysis-defects` → `pymatgenanalysisdefects`). It must be a letter and then up to 24 letters or digits. Otherwise set `BS_PROJECT` or pass `--project`.
- **Repo:** the git repo you are in, when it is the project's. Naming another project (`BS_PROJECT`, `--project`) uses the directory under `BS_REPOS` whose normalised name is the project; `bs queen` and `bs spawn` refuse to start without one and say to set `BS_REPOS`.
- **Queen:** `queen-<project>`.
- **Bead ids:** `<project>-<n>` for epics (`bs epic "<title>"` makes the next one), `<project>-<n>.<m>` for their children (`bs bead new --parent <epic> "<title>"`). `bs bead ls` lists only this project's beads; the queen and workers are told to use these instead of `bd list`/`bd create`.
- **Worker:** the bead id with `.` → `-` (`bees-2.1` → `bees-2-1`). That one name is its Claude session, herdr agent, git branch, worktree directory (`<repo>-WT/<name>`) and address for `bs send`.
- Session names never contain `:`.

## Run a queen and workers

```sh
cd ~/repos/<you>/<project>
bs queen                       # execs claude --name queen-<project> in the repo; restart it this way, not with a bare claude --resume
# inside the queen:
bs epic "ringing"              # → <project>-2
bs bead new --parent <project>-2 --description "…" "…"   # → <project>-2.1
bs bead ls                     # this project's beads that aren't closed
bs spawn --label "ring on send" <project>-2.1   # worker <project>-2-1 in its own herdr workspace and worktree
bs send <project>-2-1 "…"      # store, then ring it awake; --now interrupts its current turn
bs inbox                       # your unread messages; --peek leaves them unread
bs status                      # every session the logbook knows: running or not, unread
bs end <project>-2-1           # remove its workspace; a git worktree's branch is kept; refuses uncommitted work without --force
bs prune                       # delete ended workers' branches once merged; --dry-run reports only
```

A first spawn needs `--label`: 1–4 lowercase words naming the work (the queen writes it from the bead), shown as the herdr workspace name `<project> · <label>`. Label the queen's own workspace to match: `herdr workspace create --cwd <repo> --label "<project> · queen"`. `bs spawn` on a worker whose Claude has exited keeps its recorded label (leave `--label` off) and resumes it in the same workspace with `claude --continue`, or starts it fresh there if it never read its brief (its first start failed). Several beads go in one call, each after its own `--label`: `bs spawn --label "sort fruits" <project>-1.1 --label "dedupe todo" <project>-1.2` starts the workers in parallel and prints one line per bead. A mistake in the command (a bad or missing label, a bead named twice) starts nothing (exit 2); a worker that fails to start gets its own line while the rest start (exit 1). Each start takes about 10 s: herdr gives up waiting for an idle prompt while the worker gets on with its doorbell, and bs then confirms the worker from Claude's session registry.

After merging changes to bs, rebuild it from `main` with `go install ./cmd/bs`; in a bees fleet that is the queen's job. Workers never install bs: they build into their worktree (`go build -o bin/bs ./cmd/bs`) and run `./bin/bs`. Inside this repo, bs warns when the shared bs was built from a commit behind `main` or not on it (a branch build), and when a worktree's `bin/bs` doesn't match its checkout; a worker is told to ask the queen rather than reinstall. `bs version` prints the commit it was built from.

## Folder trust

Claude asks whether to trust a repo the first time it opens one, and its worktrees share the answer. Open the repo in Claude once (`bs queen` does) before spawning. If a worker is spawned into an untrusted repo, `bs spawn` warns and the worker waits at the trust prompt in its herdr pane until you answer it.

## Worker permissions

`bs spawn` starts each worker with its own `--settings` JSON, the only per-session place Claude's auto mode classifier reads `autoMode` from (it ignores the project's `.claude/settings*.json`):

- `autoMode.environment` and `autoMode.allow` name the worker's worktree and branch and let it run the repo's build/lint/test commands there, push its own branch and update its own PR. The environment adds that the keeper merges every PR only when `gh` reports the default branch protected. Each list keeps `"$defaults"`, so the built-in rules (force push, exfiltration) stay.
- `permissions.deny` blocks, before the classifier, the guards from `[guards] deny` in bs's config. Left empty, it is the delivery kind's default (ADR-0009). Under `github_pr`: force-push, pushes naming `main`, `master`, the repo's real default branch (origin/HEAD) or `refs/heads/` one of them, `gh pr merge`, `gh pr review --approve`, and `gh api` merge calls. Under `manual`: pushes to those default branches only. Bash rules match commands as written, so they cover `git push …`, `git -C <dir> push …`, `git -c k=v push …`, `/usr/bin/git push …` and `command git push …`, not every spelling.
- `[guards] allow_merge_into = ["pilot/*"]` lets a project's workers merge their own PRs into matching base branches (`github_pr` only). A deny rule can't see a PR's base, so it lifts the `gh pr merge` deny and tells the classifier and the role prompts which bases are allowed. A glob that matches a default branch is refused, and since only branch protection then keeps a worker's merge off the default branch, the grant fails closed: `bs queen` and `bs spawn` refuse it unless `gh` confirms the default branch is protected. `bs config` prints the effective guards.

`worker.path = ["<dir>"]` in a project file is `bs spawn --path` for every spawn that passes none: a live test's toy uses it so its workers run the build under test.

**Protect `main` on GitHub** (Settings → Branches: require a pull request). The deny rules are a speed bump that a shell can spell around; branch protection is the guard that keeps a worker's push off `main`.

No sandbox: the keeper chose not to run workers in Claude's bubblewrap sandbox (bees-1.51). To see what a worker's classifier uses, pass its settings back in: `claude --settings '<json from its spawn>' auto-mode config` (plain `claude auto-mode config` shows only your own settings).

## A worker without herdr

Any Claude session can take part if it was started with a name and accepts cross-session messages:

```sh
claude --name worker-a --settings '{"crossSessionInbound":"accept"}'
```

Without `crossSessionInbound: accept`, a session in bypass mode holds rung messages instead of acting on them. Outside Claude, say who you are with `--as <name>` or `BS_SESSION`. Inside a named Claude session bs takes your name from Claude's registry and refuses an `--as` naming anyone else, and `bs send` rings only the project's queen and the workers it spawned; mail for anyone else is stored without a ring. That refusal stops a careless `--as`, not a determined one: a bs detached from its session (`( bs … & )`, `nohup`, `setsid`) has no Claude ancestor left, so `--as` is taken as given, and any process of your user can write the logbook directly.

## Check it works

```sh
go test ./...                      # includes a writer killed mid-send losing nothing
scripts/e2e.sh <throwaway bead>    # real queen + worker in herdr: wake ≤ 2 s, round trip, queued mail
```

`scripts/e2e.sh` writes to the project's logbook (as `e2e-queen`, `e2e-keeper` and the worker) and cleans up its workspaces.

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

Tests that need a real herdr skip when none is running; `scripts/e2e.sh` stays local-only.

`.pre-commit-config.yaml` runs the lint job (golangci-lint on changed lines only) on every commit; tests stay in CI. Install the hook once per clone with `uv tool install pre-commit && pre-commit install`; it lands in the shared `.git/hooks`, so every herdr worktree of that clone runs it too (`uvx pre-commit install` works but pins a uv cache path that `uv cache clean` breaks). Check everything with `pre-commit run --all-files`.

## Releases

Tag by hand and push the tag (`git tag v0.1.0 && git push origin v0.1.0`). `.github/workflows/release.yml` runs GoReleaser (`.goreleaser.yaml`): `bs` for linux and darwin, amd64 and arm64, `CGO_ENABLED=0`, a changelog from the commits since the last tag, and `SHA256SUMS`. `bs version` prints the commit from Go's VCS stamp (`-buildvcs=true` fails the build without one). Dry run: `goreleaser release --snapshot --clean`.
