# Setting up pns

Linux only. Needs Go, [Claude Code](https://code.claude.com) with cross-session messaging (2.1.296 or later), [herdr](https://herdr.dev) for spawning peons, and `bd` for beads.

## Install

```sh
go install ./cmd/pns     # from this repo
pns init                 # optional: creates ~/.local/share/peons/<project>.db (any pns command does)
```

## Names

- **Project:** the basename of the git repo you are in (a worktree counts as its main repo), lowercased with `-` → `_`. It must be a letter and then up to 24 letters, digits or `_`. Otherwise set `PNS_PROJECT` or pass `--project`.
- **Chief:** `chief-<project>`.
- **Bead ids:** `<project>-<n>` for epics (`pns epic "<title>"` makes the next one), `<project>-<n>.<m>` for their children (`bd create --parent <epic> --force …`).
- **Peon:** the bead id with `.` → `-` (`peons-2.1` → `peons-2-1`). That one name is its Claude session, herdr agent, git branch, worktree directory (`<repo>-WT/<name>`) and address for `pns send`.
- Session names never contain `:`.

## Run a chief and peons

```sh
cd ~/repos/<you>/<project>
pns chief                       # execs claude --name chief-<project> in the repo
# inside the chief:
pns epic "ringing"              # → <project>-2
bd create --parent <project>-2 --force --title "…" --description "…"   # → <project>-2.1
pns spawn <project>-2.1         # peon <project>-2-1 in its own herdr workspace and worktree
pns send <project>-2-1 "…"      # store, then ring it awake; --now interrupts its current turn
pns inbox                       # your unread messages; --peek leaves them unread
pns status                      # every session the logbook knows: running or not, unread
pns end <project>-2-1           # remove its workspace; its branch is deleted if merged
```

`pns spawn` on a peon whose Claude has exited resumes it in the same workspace with `claude --continue`.

## Folder trust

Claude asks whether to trust a repo the first time it opens one, and its worktrees share the answer. Open the repo in Claude once (`pns chief` does) before spawning. If a peon is spawned into an untrusted repo, `pns spawn` warns and the peon waits at the trust prompt in its herdr pane until you answer it.

## A peon without herdr

Any Claude session can take part if it was started with a name and accepts cross-session messages:

```sh
claude --name peon-a --settings '{"crossSessionInbound":"accept"}'
```

Without `crossSessionInbound: accept`, a session in bypass mode holds rung messages instead of acting on them. Outside Claude, say who you are with `--as <name>` or `PNS_SESSION`.

## Check it works

```sh
go test ./...                      # includes a writer killed mid-send losing nothing
scripts/e2e.sh <throwaway bead>    # real chief + peon in herdr: wake ≤ 2 s, round trip, queued mail
```

`scripts/e2e.sh` writes to the project's logbook (as `e2e-chief`, `e2e-captain` and the peon) and cleans up its workspaces.
