# Set up bees with an agent

Steps any coding agent follows to set bees up for its keeper (the human). Done means the last step's round trip ran in every project the keeper set up. Work in order; each step checks first and installs or writes only what its check finds missing. [`SETUP.md`](SETUP.md) is the reference for names, permissions and CI.

**Before the swap (bees-13.9)** the rebuilt bs is `bsx` (`cmd/bsx`, [ADR-0010](adr/0010-core-rebuild.md)). It runs only on a herdr server started with `herdr --session <session>`, on that session's own config dir, `~/.config/bees/sessions/<session>/`, and refuses the default herdr session and the real logbooks in `~/.local/share/bees`. The queen and workers type `bsx`, so the session's server puts the build first on its panes' PATH (step 4). Pick a session name with the keeper (lowercase letters and digits, e.g. `hive`); `<session>` below is that name.

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
| Harness (machine-wide) | `claude_code` | The program every queen and worker starts; the only one yet (omp: bees-12.8, agy: bees-12.12). |
| Projects | a name and a git checkout each | Its name is the checkout's directory name, lowercased with `-` and `_` dropped; bsx finds it from the directory it runs in. |
| Workspace | `git_worktree` | Each worker gets its own worktree and branch under `<repo>-WT/`. That is the only kind bsx makes yet: a directory that isn't a git checkout's top level (`none`, bees-13.10) and other VCS such as jj or a CitC client (`command`, bees-13.11) have no working path, and `bsx doctor` and `bsx spawn` refuse them. |
| Goals | `pr`, `branch`, `verdict`, `note`, `beads`, … | The queen names one per spawn (`bsx spawn --goal`). `pr`, `push=` and `review=` need the repo's GitHub remote; the others need no network. |

**Check:** the keeper answered each question, and each project's directory is a git checkout's top level (`git -C <dir> rev-parse --show-toplevel` prints `<dir>`). For a project that will use `pr` goals, `git -C <dir> remote -v` names GitHub; if not, ask the keeper to add one (`git remote add origin <url>`) or to use only `branch`, `verdict`, `note` and `beads` goals for now (`bsx doctor` warns until a remote exists).

## 3. Install

Install what step 1 found missing, then rerun its check.

1. **Go**, at least `go.mod`'s (<https://go.dev/dl/>, or `mise use -g go@latest`).
2. **Claude Code** 2.1.296 or later (`claude update`).
3. **herdr**: `curl -fsSL https://herdr.dev/install.sh | sh`.
4. **bd** 1.1.0 or later (<https://github.com/steveyegge/beads>), with one local store every repo shares. If `bd where` prints a store, keep it. Otherwise add `export BEADS_DIR="${BEADS_DIR:-$HOME/.local/share/beads/.beads}"` to a file non-interactive shells read, then `mkdir -p ~/.local/share/beads && cd ~/.local/share/beads && bd init --quiet --non-interactive --skip-agents --skip-hooks`. The store has no remote: leave `bd dolt push`, `pull` and `remote add` unused.
5. **gh**, for `pr` goals: <https://cli.github.com>, then the keeper runs `gh auth login`.
6. **bsx**, in a directory of its own that only the session's server puts on PATH (never `go install`: the shared `bs` stays as it is until the swap):

   ```sh
   cd <bees> && git switch main && git pull
   mkdir -p ~/.local/share/bees-<session>/bin
   go build -o ~/.local/share/bees-<session>/bin/bsx ./cmd/bsx
   ```

**Check:** `~/.local/share/bees-<session>/bin/bsx config` from a project's repo prints `refuses the default config dir`: that is the right binary, outside its session.

## 4. Start the session and write the config

The keeper starts the session's server with the bsx directory first on its PATH, so every pane it opens runs it:

```sh
PATH=~/.local/share/bees-<session>/bin:$PATH herdr --session <session>
```

(a terminal for the keeper to watch; `herdr --session <session> server` runs it with no window). From your own shell, with `HERDR_SESSION=<session>` and inside a project's repo, write the session's global config. `bsx config init` writes every key the file may hold, each under a comment saying what it means and commented out at its default; its flags set what you decided. It never overwrites a file: edit an existing one by hand.

```sh
cd <project dir>
HERDR_SESSION=<session> ~/.local/share/bees-<session>/bin/bsx config init --logbook-dir ~/.local/share/bees-<session>/logbooks
# a project that needs a key of its own:
HERDR_SESSION=<session> ~/.local/share/bees-<session>/bin/bsx config init --project
```

Keys `bsx config` marks `not read by bsx yet` (the models, `worker.path`) do nothing before the swap: leave them unset.

Your own shell may carry `BS_*` variables from another bees session; `bsx doctor` names each one. Run bsx from your shell with them unset (`env -u BS_PROJECT …`). A `HERDR_PANE_ID` or `HERDR_SOCKET_PATH` of the keeper's own herdr is harmless for the commands you run from your shell here (`config`, `config init`, `status`, `end`, `prune`): `HERDR_SESSION` picks the server. `--logbook-dir` is required before the swap: bsx refuses the real logbooks' dir. A key with no flag is in the written file, commented out: uncomment it there.

**Check:** `HERDR_SESSION=<session> …/bsx config`, run inside each project's repo, exits 0 and shows `# project: <name>`, `# repo: <dir>`, `# workspace kind: …`, and each value with its source (`# global <file>`, `# project <file>` or `# default`).

## 5. Harness setup (claude_code)

- Workers need nothing in Claude's settings: `bsx spawn` passes each its own `--settings`. Ask the keeper: **will you run the queen in bypass permission mode?** If yes, offer to add `"crossSessionInbound": "accept"` to `~/.claude/settings.json`, or rung messages wait unread.
- Ask which permission mode workers should start in: a worker at a permission prompt waits in its pane. Workers take the keeper's default mode from `~/.claude/settings.json`; offer `"permissions": {"defaultMode": "auto"}` there, which keeps them moving. A keeper who declines keeps their current default.
- **Folder trust.** Claude asks once per folder whether to trust it, and the worktrees of a repo share its answer. The first `bsx queen` in a project may ask (not if the folder or one above it is trusted). The cursor starts on **No, exit**: a bare Enter quits the queen. The keeper picks **Yes, I trust this folder**, or, if they hand it to you, `herdr --session <session> pane send-keys "$pane" Down Enter`.

**Check:** the keeper answered both questions.

## 6. Verify

Every herdr call names the session: `herdr --session <session> …`; a bare `herdr` is the keeper's own server. For each project, open a workspace on its directory and keep its pane id:

```sh
pane=$(herdr --session <session> workspace create --cwd <dir> --label "<project> · queen" --no-focus | jq -r .result.root_pane.pane_id)
herdr --session <session> pane run "$pane" "{ command -v bsx; bsx doctor; echo \"exit \$?\"; } > <scratch>/doctor-<project>.out 2>&1"   # then read the file
herdr --session <session> pane run "$pane" "bsx queen"
```

`command -v bsx` must name `~/.local/share/bees-<session>/bin/bsx`; if the pane's shell rc put another first, run `export PATH=~/.local/share/bees-<session>/bin:$PATH` in the pane before the queen: her Claude, and every bsx call she makes, inherit the pane's PATH. `<scratch>` is any directory of yours. `bsx doctor` prints one line per check, `ok`, `WARN` or `FAIL`, and exits 1 on any FAIL, naming what to fix: the config, the project and its logbook dir, the harness and its ring sockets' modes, the herdr server (a `WARN … server … is older than client` means it still runs the binary it started from: the keeper restarts it when no session they need runs), bd, gh and the GitHub remote, and stray `BS_*` variables. Fix every FAIL (the file ends with `exit 0`) before `bsx queen`.

The queen is a Claude session, not a shell: type to her with `herdr --session <session> agent prompt "$pane" "<request>"`, then `herdr --session <session> agent wait "$pane" --until working --timeout 20000` and `herdr --session <session> agent wait "$pane" --timeout 600000`, and read her with `herdr --session <session> pane read "$pane" --source recent --lines 40`. bsx has no `bsx epic` or `bsx bead` yet (they come with the swap), so file the check's beads yourself, from any shell (the store is shared; `--force` because the store's own prefix differs):

```sh
bd create --force --id <project>-1 --type epic "setup check"        # an id bd show doesn't know yet; else <project>-2, …
bd create --force --parent <project>-1 "say yes and stop" --description "Answer yes with bsx done --answer yes --report \"setup check\", then stop."
```

Then ask her for the round trip in one line, and say you will clean up yourself: "spawn `<project>-1.1` with `--goal verdict` and label `setup check`, then tell me when it hands off". She runs `bsx spawn --goal verdict --label "setup check" <project>-1.1`.

`bsx spawn` may warn that herdr timed out waiting for the agent and that bsx `adopted` it: the worker started all the same, nothing to do. The worker opens in a new workspace, reads its brief and hands off with `bsx done`, which rings the queen. Read her pane after your first wait: a quick worker's handoff may already show. If it doesn't, the queen went idle after `bsx spawn`: wait again (`--until working --timeout 300000`, then `--timeout 600000`) for the turn the doorbell starts, and read her pane again. `HERDR_SESSION=<session> …/bsx status` from your shell, in the project's repo, then shows the worker with `HANDOFF done …` and `COMPLETE yes (done)`. **Check:** `bsx doctor` passed, and the queen received the handoff through a doorbell.

Clean up from your own shell inside the project's repo, as the queen (`--as`, since your shell is no recorded pane), not by hand:

```sh
HERDR_SESSION=<session> …/bsx end --as queen-<project> <project>-1-1    # complete (its verdict landed at handoff), so it ends
HERDR_SESSION=<session> …/bsx prune --as queen-<project>                # removes its worktree and branch; a branch with unmerged commits it keeps, saying so
bd close <project>-1.1 <project>-1
```

Leave the queen running: she is the keeper's, in the `<project> · queen` workspace. **Check:** `bsx status` shows the worker `ended`, its workspace `(removed)`, with nothing `stuck:`, and both beads are closed.

Tell the keeper setup is done, and point them at [`SETUP.md`](SETUP.md).
