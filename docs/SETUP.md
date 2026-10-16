# Setting up bs

Linux only. Needs Go, [Claude Code](https://code.claude.com) with cross-session messaging (2.1.296 or later), [herdr](https://herdr.dev) for spawning bees, and `bd` for beads.

## Install

```sh
go install ./cmd/bs     # from this repo
bs init                 # optional: creates ~/.local/share/bees/<project>.db (any bs command does)
```

## Names

- **Project:** the basename of the git repo you are in (a worktree counts as its main repo), lowercased with `-` → `_`. It must be a letter and then up to 24 letters, digits or `_`. Otherwise set `BS_PROJECT` or pass `--project`.
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
bs spawn <project>-2.1         # bee <project>-2-1 in its own herdr workspace and worktree
bs send <project>-2-1 "…"      # store, then ring it awake; --now interrupts its current turn
bs inbox                       # your unread messages; --peek leaves them unread
bs status                      # every session the logbook knows: running or not, unread
bs end <project>-2-1           # remove its workspace; its branch is deleted if merged
```

`bs spawn` on a bee whose Claude has exited resumes it in the same workspace with `claude --continue`.

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
