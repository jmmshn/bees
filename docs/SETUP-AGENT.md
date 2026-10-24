# Set up bees with an agent

Steps any coding agent follows to set bees up for its keeper (the human). Done means the last step's round trip ran in every project the keeper set up. Work in order; each step checks first and installs or writes only what its check finds missing. [`SETUP.md`](SETUP.md) is the reference for names, permissions and CI.

bs ([ADR-0010](adr/0010-core-rebuild.md)) runs on the keeper's own herdr server, reads `~/.config/bees/`, and keeps its logbooks in `~/.local/share/bees/` unless the config says otherwise. (A second fleet runs on a `herdr --session <session>` server instead, with its own config dir, `~/.config/bees/sessions/<session>/`, which `bs config init` writes from a shell with `HERDR_SESSION=<session>` set; the [`live-test`](../.claude/skills/live-test/SKILL.md) skill does this.)

Two rules hold throughout:

- The keeper runs anything interactive (an installer piping to `sh`, attaching to herdr, a login) in a shell where you can read its output; in Claude Code that is a `! <command>` line.
- Ask before writing to the keeper's own files (shell rc files, a harness's settings), and say which line you will add.

## 1. Survey

Run (`<bees>` is a clone of the bees repo, `git clone https://github.com/jmmshn/bees`, whose `go.mod` names the Go it needs):

```sh
uname -s; go version; grep '^go ' <bees>/go.mod; git --version; jq --version; herdr --version; bd --version; bd where; gh --version | head -1; claude --version
```

`uname -s` must say `Linux` or `Darwin`. **Check:** the keeper has seen one line per tool (present with its version, or missing), whether Go is at least `go.mod`'s, and which steps that leaves.

## 2. Decide

Settle these with the keeper, one batch of questions:

| Question | Values | Notes |
|---|---|---|
| Harness (machine-wide) | `claude_code` \| `agy` | The program every queen and worker starts (omp: bees-12.8). Keep `claude_code` and run agy per spawn (`bs spawn --harness agy`); a global `agy` moves the queen too, untested (bees-13.13.11). agy setup: [SETUP.md](SETUP.md#agy-workers). |
| Projects | a name and a git checkout each | Its name is the checkout's directory name, lowercased with `-` and `_` dropped; bs finds it from the directory it runs in. |
| Workspace | `git_worktree` \| `command` | `git_worktree`: each worker gets its own worktree and branch under `<repo>-WT/`. `command`: another VCS (jj, hg, a cloud client with CLs) through the keeper's own argv and a custom goal ([SETUP.md](SETUP.md#other-vcs-a-command-workspace-and-a-custom-goal)). A directory that isn't a git checkout's top level (`none`, bees-13.10) has no working path yet: `bs doctor` and `bs spawn` refuse it. |
| Goals | `pr`, `branch`, `verdict`, `note`, `beads`, … | The queen names one per spawn (`bs spawn --goal`). `pr`, `push=` and `review=` need the repo's GitHub remote; the others need no network. |

**Check:** the keeper answered each question, and each project's directory is a git checkout's top level (`git -C <dir> rev-parse --show-toplevel` prints `<dir>`). For a project that will use `pr` goals, `git -C <dir> remote -v` names GitHub; if not, ask the keeper to add one (`git remote add origin <url>`) or to use only `branch`, `verdict`, `note` and `beads` goals for now (`bs doctor` warns until a remote exists).

## 3. Install

Install what step 1 found missing, then rerun its check.

1. **Go**, at least `go.mod`'s (<https://go.dev/dl/>, or `mise use -g go@latest`).
2. **Claude Code** 2.1.296 or later (`claude update`).
3. **herdr**: `curl -fsSL https://herdr.dev/install.sh | sh`.
4. **bd** 1.1.0 or later (<https://github.com/steveyegge/beads>), with one local store every repo shares. If `bd where` prints a store, keep it. Otherwise add `export BEADS_DIR="${BEADS_DIR:-$HOME/.local/share/beads/.beads}"` to a file non-interactive shells read, then `mkdir -p ~/.local/share/beads && cd ~/.local/share/beads && bd init --quiet --non-interactive --skip-agents --skip-hooks`. The store has no remote: leave `bd dolt push`, `pull` and `remote add` unused.
5. **gh**, for `pr` goals: <https://cli.github.com>, then the keeper runs `gh auth login`.
6. **bs**, from `main` (only the keeper, or a fleet's queen, ever installs it):

   ```sh
   cd <bees> && git switch main && git pull && go install ./cmd/bs
   ```

**Check:** `command -v bs` names the binary `go install` wrote (`$(go env GOPATH)/bin/bs` unless `GOBIN` is set), and `bs version` prints `git -C <bees> rev-parse HEAD`.

## 4. Write the config

The keeper runs herdr (`herdr` in a terminal of theirs). From your own shell, inside a project's repo, write the global config. `bs config init` writes every key the file may hold, each under a comment saying what it means and commented out at its default; its flags set what you decided. It never overwrites a file: edit an existing one by hand.

```sh
cd <project dir>
bs config init
# a project that needs a key of its own:
bs config init --project
```

For another VCS, add the `[workspace.command]`, `[checks.<name>]` and `[goal.<name>]` tables by hand ([SETUP.md](SETUP.md#other-vcs-a-command-workspace-and-a-custom-goal) has jj, hg and CL examples), then run `bs doctor`.

Your own shell may carry `BS_*` variables from another bees session; `bs doctor` names each one. Run bs from your shell with them unset (`env -u BS_PROJECT …`). A key with no flag is in the written file, commented out: uncomment it there.

**Check:** `bs config`, run inside each project's repo, exits 0 and shows `# project: <name>`, `# repo: <dir>`, `# workspace kind: …`, and each value with its source (`# global <file>`, `# project <file>` or `# default`).

## 5. Harness setup (claude_code)

- Workers need nothing in Claude's settings: `bs spawn` passes each its own `--settings`. Ask the keeper: **will you run the queen in bypass permission mode?** If yes, offer to add `"crossSessionInbound": "accept"` to `~/.claude/settings.json`, or rung messages wait unread.
- Ask which permission mode workers should start in: a worker at a permission prompt waits in its pane. Workers take the keeper's default mode from `~/.claude/settings.json`; offer `"permissions": {"defaultMode": "auto"}` there, which keeps them moving. A keeper who declines keeps their current default.
- **Folder trust.** Claude asks once per folder whether to trust it, and the worktrees of a repo share its answer. The first `bs queen` in a project may ask (not if the folder or one above it is trusted). The cursor starts on **No, exit**: a bare Enter quits the queen. The keeper picks **Yes, I trust this folder**, or, if they hand it to you, `herdr pane send-keys "$pane" Down Enter`.

**Check:** the keeper answered both questions.

## 6. Verify

For each project, open a workspace on its directory and keep its pane id:

```sh
pane=$(herdr workspace create --cwd <dir> --label "<project> · queen" --no-focus | jq -r .result.root_pane.pane_id)
herdr pane run "$pane" "{ command -v bs; bs doctor; echo \"exit \$?\"; } > <scratch>/doctor-<project>.out 2>&1"   # then read the file
herdr pane run "$pane" "bs queen"
```

`command -v bs` must name the binary step 3 installed; if the pane's shell rc put another first, fix the pane's PATH before the queen: her Claude, and every bs call she makes, inherit it. `<scratch>` is any directory of yours. `bs doctor` prints one line per check, `ok`, `WARN` or `FAIL`, and exits 1 on any FAIL, naming what to fix: the config, the project and its logbook dir, the harness and its ring sockets' modes, the herdr server (a `WARN … server … is older than client` means it still runs the binary it started from: the keeper restarts it when no session they need runs), bd, gh and the GitHub remote, and stray `BS_*` variables. Fix every FAIL (the file ends with `exit 0`) before `bs queen`.

The queen is a Claude session, not a shell: type to her with `herdr agent prompt "$pane" "<request>"`, then `herdr agent wait "$pane" --until working --timeout 20000` and `herdr agent wait "$pane" --timeout 600000`, and read her with `herdr pane read "$pane" --source recent --lines 40`. File the check's beads yourself, from your shell in the project's repo:

```sh
bs epic "setup check"                                                # prints the epic's id, <project>-N
bs bead new --parent <project>-N --description "Answer yes with bs done --answer yes --report \"setup check\", then stop." "say yes and stop"   # → <project>-N.1
```

Then ask her for the round trip in one line, and say you will clean up yourself: "spawn `<project>-N.1` with `--goal verdict` and label `setup check`, then tell me when it hands off". She runs `bs spawn --goal verdict --label "setup check" <project>-N.1`.

`bs spawn` may warn that herdr timed out waiting for the agent and that bs `adopted` it: the worker started all the same, nothing to do. The worker opens in a new workspace, reads its brief and hands off with `bs done`, which rings the queen. Read her pane after your first wait: a quick worker's handoff may already show. If it doesn't, the queen went idle after `bs spawn`: wait again (`--until working --timeout 300000`, then `--timeout 600000`) for the turn the doorbell starts, and read her pane again. `bs status` from your shell, in the project's repo, then shows the worker with `HANDOFF done …` and `COMPLETE yes (done)`. **Check:** `bs doctor` passed, and the queen received the handoff through a doorbell.

Clean up from your own shell inside the project's repo, as the keeper (no session started your shell), not by hand:

```sh
bs end <project>-N-1    # complete (its verdict landed at handoff), so it ends
bs prune                # removes its worktree and branch; a branch with unmerged commits it keeps, saying so
bd close <project>-N.1 <project>-N
```

Leave the queen running: she is the keeper's, in the `<project> · queen` workspace. **Check:** `bs status` shows the worker `ended`, its workspace `(removed)`, with nothing `stuck:`, and both beads are closed.

Tell the keeper setup is done, and point them at [`SETUP.md`](SETUP.md).
