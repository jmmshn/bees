---
name: setup-bees
description: Walk a new user through installing and checking everything bees needs (Go, Claude Code, herdr, bd, bs, pre-commit). Use when someone wants to set up, install or try bees/bs on a machine, or when bs fails because a prerequisite is missing.
---

# Set up bees

Done means every step below ends on its check passing on the user's machine, and the last step's round trip ran. Work through the steps in order; each one first checks, and installs only what the check finds missing. [`docs/SETUP.md`](../../../docs/SETUP.md) is the reference for names, commands and CI; read it when a step points there.

The user runs anything interactive (an installer piping to `sh`, `herdr` itself, a login) with a `! <command>` line, so its output lands in the conversation. Ask before writing to their config files (`~/.claude/settings.json`, shell rc files) and say what line you will add.

## 1. Survey

Run, together:

```sh
uname -s; go version; claude --version; herdr --version; bd --version; jq --version; gh --version | head -1; command -v bs && bs version
```

`uname -s` must say `Linux`; bs reads `/proc` and stops there. Report one line per tool: present with version, or missing. **Check:** the user has seen the list and which steps it leaves.

## 2. Go

Needs at least the `go` version in [`go.mod`](../../../go.mod). Install from <https://go.dev/dl/> or their version manager (`mise use -g go@latest`). `$(go env GOPATH)/bin` must be on `PATH`, since `go install` puts `bs` there. **Check:** `go version` meets `go.mod`, and `echo $PATH` contains `$(go env GOPATH)/bin`.

## 3. Claude Code

Needs 2.1.296 or later: cross-session messaging, which the doorbell rides on. Update with `claude update`.

Bees need nothing in Claude's settings files: `bs spawn` passes `--settings '{"crossSessionInbound":"accept"}'` to each bee. The queen, started by `bs queen`, gets no such flag; in bypass permission mode Claude holds incoming messages instead of acting on them. If the user runs the queen in bypass mode, offer to add `"crossSessionInbound": "accept"` to `~/.claude/settings.json`. bs needs no Claude plugin, hook or MCP server.

**Check:** `claude --version` ≥ 2.1.296, and the user has answered the bypass-mode question.

## 4. herdr

bs drives herdr to create each bee's worktree and pane and to start Claude in it. Install with `! curl -fsSL https://herdr.dev/install.sh | sh` (Homebrew, mise and Nix: <https://herdr.dev/docs/install/>). bs needs no herdr config and no herdr integration; the defaults work.

bs talks to a running herdr server, so the user starts one with `herdr` in a terminal and runs the queen inside it. If `HERDR_ENV=1` is set, this session already runs inside herdr; skip starting it and still run the check.

**Check:** `herdr status` shows `server: status: running`.

## 5. bd (beads)

bs files and reads work through `bd`: `bs epic` and `bs bead new` create beads, and `bs spawn` reads a bead as the bee's brief. Install from <https://github.com/steveyegge/beads>. Then `bd where` (run inside the project) must find a store: either `bd init` in the project, or one store shared by every repo with `BEADS_DIR=<dir>/.beads` exported from the shell profile.

**Check:** `bd where` in the project prints a store, and `bd list --limit 1` exits 0.

## 6. bs

From a clone of the bees repo:

```sh
go install ./cmd/bs
```

Rebuild the same way after pulling bees; inside the bees repo `bs` warns when it was built from a commit behind `main`. In the user's project, read the **Names** section of [`docs/SETUP.md`](../../../docs/SETUP.md) with them: the project name comes from the repo's directory, and an unusual name needs `BS_PROJECT`.

`bs prune` asks `gh` which branches merged; install and log in to [GitHub CLI](https://cli.github.com) (`! gh auth login`) if the user will prune.

**Check:** `bs version` prints a commit, and `bs status` in the project exits 0.

## 7. Folder trust

Claude asks once per repo whether to trust it, and the repo's worktrees share the answer. A bee spawned into an untrusted repo waits at that prompt in its pane. Have the user open the project in Claude once: `bs queen` from the project directory, inside herdr, does it. **Check:** the user answered the trust prompt (or saw none).

## 8. pre-commit (contributors to bees only)

Skip unless the user will change bees itself.

```sh
uv tool install pre-commit && pre-commit install
pre-commit run --all-files
```

**Check:** `pre-commit run --all-files` passes in the bees repo.

## 9. Round trip

Prove the doorbell works end to end. Inside the queen session (`bs queen`, in herdr):

```sh
bs epic "setup check"                                    # → <project>-N
bs bead new --parent <project>-N "say hello and stop"    # → <project>-N.1
bs spawn --label "setup check" <project>-N.1
```

The bee starts in a new herdr workspace, reads its brief, and should reply; `bs send <bee> "reply to me with bs send: pong"` prompts it if it does not. The queen is woken by a `[bs] 1 unread` doorbell with nobody typing; `bs inbox` shows the reply. Then clean up: `bs end <bee>`, `bd close <project>-N.1 <project>-N`.

In the bees repo itself, `scripts/e2e.sh <throwaway bead>` runs the same check and measures the wake time; it needs `jq`.

**Check:** the queen received the bee's reply via a doorbell, `herdr workspace list` no longer shows the bee's workspace, and both throwaway beads are closed. Tell the user setup is done and point them at the **Run a queen and bees** section of `docs/SETUP.md`.
