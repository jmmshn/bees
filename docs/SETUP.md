# Setting up bs

Linux only. Needs Go, [Claude Code](https://code.claude.com) with cross-session messaging (2.1.296 or later), [herdr](https://herdr.dev) for spawning bees, and `bd` for beads.

For a guided setup, run the [`setup-bees`](../.claude/skills/setup-bees/SKILL.md) skill in Claude Code from this repo (`/setup-bees`): it checks and installs each piece, then runs a queen → bee round trip.

## Install

```sh
go install ./cmd/bs     # from this repo
bs init                 # optional: creates ~/.local/share/bees/<project>.db (any bs command does)
```

## Names

- **Project:** the basename of the git repo you are in (a worktree counts as its main repo), lowercased with `-` and `_` dropped (`pymatgen-analysis-defects` → `pymatgenanalysisdefects`). It must be a letter and then up to 24 letters or digits. Otherwise set `BS_PROJECT` or pass `--project`.
- **Queen:** `queen-<project>`.
- **Bead ids:** `<project>-<n>` for epics (`bs epic "<title>"` makes the next one), `<project>-<n>.<m>` for their children (`bs bead new --parent <epic> "<title>"`). `bs bead ls` lists only this project's beads; the queen and bees are told to use these instead of `bd list`/`bd create`.
- **Bee:** the bead id with `.` → `-` (`bees-2.1` → `bees-2-1`). That one name is its Claude session, herdr agent, git branch, worktree directory (`<repo>-WT/<name>`) and address for `bs send`.
- Session names never contain `:`.

## Run a queen and bees

```sh
cd ~/repos/<you>/<project>
bs queen                       # execs claude --name queen-<project> in the repo; restart it this way, not with a bare claude --resume
# inside the queen:
bs epic "ringing"              # → <project>-2
bs bead new --parent <project>-2 --description "…" "…"   # → <project>-2.1
bs bead ls                     # this project's beads that aren't closed
bs spawn --label "ring on send" <project>-2.1   # bee <project>-2-1 in its own herdr workspace and worktree
bs send <project>-2-1 "…"      # store, then ring it awake; --now interrupts its current turn
bs inbox                       # your unread messages; --peek leaves them unread
bs status                      # every session the logbook knows: running or not, unread
bs end <project>-2-1           # remove its workspace; its branch is kept
bs prune                       # delete ended bees' branches once merged; --dry-run reports only
```

A first spawn needs `--label`: 1–4 lowercase words naming the work, shown as the herdr workspace name (the queen writes it from the bead). `bs spawn` on a bee whose Claude has exited keeps its recorded label (leave `--label` off) and resumes it in the same workspace with `claude --continue`, or starts it fresh there if it never read its brief (its first start failed). Several beads go in one call, each after its own `--label`: `bs spawn --label "sort fruits" <project>-1.1 --label "dedupe todo" <project>-1.2` starts the workers in parallel and prints one line per bead. A mistake in the command (a bad or missing label, a bead named twice) starts nothing (exit 2); a worker that fails to start gets its own line while the rest start (exit 1). Each start takes about 10 s: herdr gives up waiting for an idle prompt while the worker gets on with its doorbell, and bs then confirms the worker from Claude's session registry.

After merging changes to bs, rebuild it with `go install ./cmd/bs`. Inside this repo, bs warns when it was built from a commit behind `main`; `bs version` prints the commit it was built from.

## Folder trust

Claude asks whether to trust a repo the first time it opens one, and its worktrees share the answer. Open the repo in Claude once (`bs queen` does) before spawning. If a bee is spawned into an untrusted repo, `bs spawn` warns and the bee waits at the trust prompt in its herdr pane until you answer it.

## A bee without herdr

Any Claude session can take part if it was started with a name and accepts cross-session messages:

```sh
claude --name bee-a --settings '{"crossSessionInbound":"accept"}'
```

Without `crossSessionInbound: accept`, a session in bypass mode holds rung messages instead of acting on them. Outside Claude, say who you are with `--as <name>` or `BS_SESSION`.

## Check it works

```sh
go test ./...                      # includes a writer killed mid-send losing nothing
scripts/e2e.sh <throwaway bead>    # real queen + bee in herdr: wake ≤ 2 s, round trip, queued mail
```

`scripts/e2e.sh` writes to the project's logbook (as `e2e-queen`, `e2e-captain` and the bee) and cleans up its workspaces.

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

Tag by hand and push the tag (`git tag v0.1.0 && git push origin v0.1.0`). `.github/workflows/release.yml` runs GoReleaser (`.goreleaser.yaml`): `bs` for linux amd64 and arm64, `CGO_ENABLED=0`, a changelog from the commits since the last tag, and `SHA256SUMS`. `bs version` prints the commit from Go's VCS stamp (`-buildvcs=true` fails the build without one). Dry run: `goreleaser release --snapshot --clean`.
