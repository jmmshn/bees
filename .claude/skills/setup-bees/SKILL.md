---
name: setup-bees
description: Walk a new user through installing and checking everything bees needs (Go, Claude Code, herdr, a shared local bd store, bs, pre-commit). Use when someone wants to set up, install or try bees/bs on a machine, or when bs fails because a prerequisite is missing.
---

# Set up bees

Done means every step below ends on its check passing on the user's machine, and the last step's round trip ran. Work through the steps in order; each one first checks, and installs only what the check finds missing. [`docs/SETUP.md`](../../../docs/SETUP.md) is the reference for names, commands and CI; read it when a step points there.

The user runs anything interactive (an installer piping to `sh`, `herdr` itself, a login) with a `! <command>` line, so its output lands in the conversation. Ask before writing to their config files (`~/.claude/settings.json`, shell rc files) and say what line you will add.

## 1. Survey

Run, together:

```sh
uname -s; go version; go env GOBIN GOPATH; claude --version; herdr --version; bd --version; echo "BEADS_DIR=$BEADS_DIR"; jq --version; gh --version | head -1; command -v bs && bs version; command -v pre-commit
```

`uname -s` must say `Linux`; bs reads `/proc` and stops there. Report one line per tool: present with version, or missing; flag `bd` older than 1.1.0. **Check:** the user has seen the list and which steps it leaves.

## 2. Go

Needs at least the `go` version in [`go.mod`](../../../go.mod). Install from <https://go.dev/dl/> or their version manager (`mise use -g go@latest`). `go install` puts `bs` in `$(go env GOBIN)`, or `$(go env GOPATH)/bin` when `GOBIN` is empty; that directory must be on `PATH` in every shell, including the non-interactive ones Claude runs tools in (export it from `~/.zshenv` or `~/.bash_profile`, not only an rc file). mise sets `GOBIN` to the Go version's own install dir, so `mise upgrade go` leaves `bs` behind: rerun step 6 after upgrading Go. **Check:** `go version` meets `go.mod`, and `zsh -c 'echo $PATH'` (or `bash -c`) contains that directory.

## 3. Claude Code

Needs 2.1.296 or later: cross-session messaging, which the doorbell rides on. Update with `claude update`.

Workers need nothing in Claude's settings files: `bs spawn` passes each worker its own `--settings` JSON with `"crossSessionInbound": "accept"`, auto-mode `environment` and `allow` entries naming its worktree and branch (build, test and push its own branch; the keeper merges by hand), and `permissions.deny` rules against force-push, pushing to `main`/`master`, `gh pr merge` and approving PRs. The queen, started by `bs queen`, gets no such flag; in bypass permission mode Claude holds incoming messages instead of acting on them. If the user runs the queen in bypass mode, offer to add `"crossSessionInbound": "accept"` to `~/.claude/settings.json`. bs needs no Claude plugin, hook or MCP server.

Workers start in the user's default permission mode, and a worker that hits a permission prompt waits in its herdr pane until someone answers it there. Ask which mode workers should run in; to change it, offer to set `"permissions": {"defaultMode": "<mode>"}` in `~/.claude/settings.json` (`auto` keeps workers moving with a safety check on each action).

Optional: a `SessionStart` hook running `bd prime` gives every session the bd workflow primer. bees works without it; workers get their bead from `bs inbox` and `bd show`.

**Check:** `claude --version` ≥ 2.1.296, and the user has answered the bypass-mode and permission-mode questions.

## 4. herdr

bs drives herdr to create each worker's worktree and pane and to start Claude in it. Install with `! curl -fsSL https://herdr.dev/install.sh | sh` (Homebrew, mise and Nix: <https://herdr.dev/docs/install/>). bs needs no herdr config and no herdr integration; the defaults work.

bs talks to a running herdr server, so the user starts one with `herdr` in a terminal and runs the queen inside it. If `HERDR_ENV=1` is set, this session already runs inside herdr; skip starting it and still run the check.

**Check:** `herdr status` shows `server: status: running`.

## 5. bd (beads)

bs files and reads work through `bd`: `bs epic` and `bs bead new` create beads, `bs bead ls` runs `bd query`, and `bs spawn` reads a bead as the worker's brief. Needs bd 1.1.0 or later (Dolt backend; older releases lack `bd query`). Install from <https://github.com/steveyegge/beads> or `mise use -g github:steveyegge/beads@1.1.0`.

**One shared store.** Every repo uses one store at `$HOME/.local/share/beads/.beads`, so `bd` finds it from any directory, including worker worktrees. If `BEADS_DIR` is already set and `bd where` finds a store, keep it. Otherwise:

1. Add `export BEADS_DIR="${BEADS_DIR:-$HOME/.local/share/beads/.beads}"` to a file every shell reads, including non-interactive ones (`~/.zshenv`, or `~/.bash_profile` and `~/.bashrc`). Ask first.
2. Create the store: `mkdir -p ~/.local/share/beads && cd ~/.local/share/beads && bd init --quiet --non-interactive --skip-agents --skip-hooks`. bd's default embedded Dolt queues parallel writers on a lock; `bd init --server` instead runs a Dolt server that bd starts on demand (needs `dolt` on `PATH`), for many workers writing at once.
3. `bd metrics off` if the user wants nothing sent upstream.

The store is local only: it has no remote, and stays that way. Leave `bd dolt push`, `bd dolt pull` and `bd dolt remote add` unused; bees reads and writes the local store.

A user who wants the store inside the project instead runs `bd init` there and skips `BEADS_DIR`.

**Bead ids.** bs sees only beads whose id starts `<project>-` (**Names** in `docs/SETUP.md`). `bs epic` sets that id explicitly, and children of a project bead take `<parent>.<n>`, so the store's own prefix never matters for beads made through bs. A bead made with a bare `bd create` gets the store's prefix and a hash (`beads-x7k`), invisible to `bs bead ls`; bring it in with `bd rename <id> <project>-N.M`, which carries its deps and references along.

**Check:** `bd --version` ≥ 1.1.0; from the project and from an unrelated directory, `bd where` prints the same store; `bd list --limit 1` exits 0.

## 6. bs

From a clone of the bees repo:

```sh
go install ./cmd/bs
```

Rebuild the same way after pulling bees; inside the bees repo `bs` warns when it was built from a commit behind `main`. In the user's project, read the **Names** section of [`docs/SETUP.md`](../../../docs/SETUP.md) with them: the project name comes from the repo's directory, and an unusual name needs `BS_PROJECT`.

`bs prune` asks `gh` which branches merged; install and log in to [GitHub CLI](https://cli.github.com) (`! gh auth login`) if the user will prune.

**Check:** `bs version` prints a commit, and `bs status` in the project exits 0.

## 7. Folder trust

Claude asks once per repo whether to trust it, and the repo's worktrees share the answer. A worker spawned into an untrusted repo waits at that prompt in its pane. Have the user open the project in Claude once: `bs queen` from the project directory, inside herdr, does it.

mise keeps its own trust list, per path. If the project has a `mise.toml` and the user runs mise, each new worktree under `<repo>-WT/` is untrusted until `mise trust` runs in it, and its tools are missing until then; workers run `mise trust` on entering, or the user trusts the `-WT` parent by adding it to `trusted_config_paths` under `[settings]` in `~/.config/mise/config.toml`.

**Check:** the user answered the trust prompt (or saw none), and mise trust is settled for projects that have a `mise.toml`.

## 8. pre-commit (contributors to bees only)

Skip unless the user will change bees itself.

```sh
uv tool install pre-commit && pre-commit install
pre-commit run --all-files
```

`uv tool install` puts `pre-commit` on `PATH` (`~/.local/bin`), so worker worktrees can run it too; the hook lands in the clone's shared `.git/hooks`. Without it on `PATH`, `uvx pre-commit run --all-files` works for a one-off run.

**Check:** `command -v pre-commit` prints a path, and `pre-commit run --all-files` passes in the bees repo.

## 9. Round trip

Prove the doorbell works end to end. Inside the queen session (`bs queen`, in herdr):

```sh
bs epic "setup check"                                    # → <project>-N
bs bead new --parent <project>-N "say hello and stop"    # → <project>-N.1
bs spawn --label "setup check" <project>-N.1
```

The worker starts in a new herdr workspace, reads its brief, and should reply; `bs send <worker> "reply to me with bs send: pong"` prompts it if it does not. The queen is woken by a `[bs] 1 unread` doorbell with nobody typing; `bs inbox` shows the reply. Then clean up: `bs end <worker>`, `bd close <project>-N.1 <project>-N`.

In the bees repo itself, `scripts/e2e.sh <throwaway bead>` runs the same check and measures the wake time; it needs `jq`.

**Check:** the queen received the worker's reply via a doorbell, `herdr workspace list` no longer shows the worker's workspace, and both throwaway beads are closed. Tell the user setup is done and point them at the **Run a queen and workers** section of `docs/SETUP.md`.
