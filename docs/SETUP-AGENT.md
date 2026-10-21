# Set up bees with an agent

Steps any coding agent follows to set bees up for its keeper (the human), on any harness. Done means the last step's round trip ran in every project the keeper set up. Work in order; each step checks first and installs or writes only what its check finds missing. [`SETUP.md`](SETUP.md) is the reference for names, permissions and CI; read it when a step points there.

Two rules hold throughout:

- The keeper runs anything interactive (an installer piping to `sh`, `herdr` itself, a login) in a shell where you can read its output; in Claude Code that is a `! <command>` line.
- Ask before writing to the keeper's own files (shell rc files, a harness's settings), and say which line you will add.

## 1. Survey

If `bs` is on `PATH`, run `bs doctor` (one line per check: `ok`, `warn` or `FAIL`) and `go version`. Otherwise run:

```sh
uname -s; go version; herdr --version; herdr status; bd --version; bd where; gh --version | head -1; claude --version; agy --version
```

`uname -s` must say `Linux` or `Darwin`; bs runs on nothing else. **Check:** the keeper has seen one line per tool (present with its version, or missing) and which steps that leaves.

## 2. Decide

Settle three choices with the keeper, one batch of questions. Each is a key in bs's config; step 4 writes them.

| Key | Values | Pick |
|---|---|---|
| `harness` (machine-wide) | `claude_code` | The agent program every queen and worker starts in. `agy` is staged: bs refuses to start it until its probes land, and `bs doctor` says why. omp comes later. |
| `workspace.kind` | `git_worktree` (default), `none` | `git_worktree`: each worker gets its own git worktree and branch under `<repo>-WT/`. `none`: every worker shares one directory the keeper makes (a non-git folder, a CitC client), with no branch. |
| `delivery.kind` | `github_pr` (default), `manual` | `github_pr`: a worker opens a PR from its branch and the keeper merges it. `manual`: a worker reports with `bs done --report`, and the keeper lands the work. |
| `delivery.fragment` | a file path | The keeper's own wording of how work lands (a CL flow), instead of the kind's. |

`none` has no branch to open a PR from, so it takes `manual` or a `fragment`; `bs queen` and `bs spawn` refuse `none` with `github_pr`. Workspace and delivery may differ per project; the harness is one per machine. To switch harness later, `bs end` every session first: `bs queen` and `bs spawn` refuse to start beside one still running on the old harness, and `bs doctor` names it.

The keeper sees each worker in herdr's sidebar as `<project> · <label>`: nested under its repo for `git_worktree`, at the top level for `none`.

**Check:** the keeper has answered the harness, and for each project they want now: its name, its directory, its workspace kind and its delivery.

## 3. Install

Install what step 1 found missing, then rerun its check.

1. **Go**, at least the `go` version in [`go.mod`](../go.mod) (<https://go.dev/dl/>, or `mise use -g go@latest`). `go install` puts binaries in `$(go env GOBIN)`, else `$(go env GOPATH)/bin`; that directory must be on `PATH` in non-interactive shells too (export it from `~/.zshenv` or `~/.bash_profile`).
2. **The harness.** `claude_code`: Claude Code 2.1.296 or later (`claude update`).
3. **herdr**: `curl -fsSL https://herdr.dev/install.sh | sh` (other installers: <https://herdr.dev/docs/install/>). It needs no config. The keeper starts its server by running `herdr` in a terminal; every queen runs inside a herdr pane.
4. **bd** 1.1.0 or later (<https://github.com/steveyegge/beads>, or `mise use -g github:steveyegge/beads@1.1.0`), with one local store every repo shares:
   - If `bd where` already prints a store, keep it.
   - Otherwise add `export BEADS_DIR="${BEADS_DIR:-$HOME/.local/share/beads/.beads}"` to a file non-interactive shells read, then `mkdir -p ~/.local/share/beads && cd ~/.local/share/beads && bd init --quiet --non-interactive --skip-agents --skip-hooks`.
   - The store is local only: it has no remote, so leave `bd dolt push`, `pull` and `remote add` unused. `bd metrics off` if the keeper wants nothing sent upstream.
   - bs sees only beads whose id starts `<project>-`, which `bs epic` and `bs bead new` give them. A bead from a bare `bd create` gets the store's prefix (`beads-x7k`); bring it in with `bd rename <id> <project>-N.M`.
5. **bs**, from a clone of the bees repo: `go install ./cmd/bs`. Rebuild the same way after pulling, always from `main`.
6. **gh**, for `github_pr` projects: <https://cli.github.com>, then the keeper runs `gh auth login`. `bs prune` asks gh which branches merged.

**Check:** `bs doctor` shows `ok` for the harness, herdr, bd and the logbook dir. A `FAIL  herdr: server …` only means the keeper hasn't started `herdr` yet.

## 4. Write the config

bs reads `$XDG_CONFIG_HOME/bees/config.toml` (else `~/.config/bees/config.toml`) and one `projects/<project>.toml` per project. `bs config init` writes either file with every key it may hold, each under a comment saying what it means, commented out at its default; its flags set the step 2 answers. It never overwrites a file: edit an existing one by hand.

```sh
# machine-wide: the harness, and defaults for every project
bs config init --harness claude_code
# a git project on the defaults (git_worktree, github_pr) needs no file of its own
# a non-git project: the keeper makes its directory first; bs never does
mkdir -p ~/work/notes
bs config init --project notes --workspace none --delivery manual --repo ~/work/notes
```

A project's name is its git repo's directory name, lowercased with `-` and `_` dropped (**Names** in [`SETUP.md`](SETUP.md)). bs finds a git project from the directory it runs in, so run its commands inside the repo. A non-git project has no repo to find it from: pass `--project <name>` to every bs command you run by hand. `--fragment <file>` sets the keeper's own delivery wording.

**Check:** for each project, `bs config` run inside its repo (git) or `bs config --project <name>` (non-git) exits 0, shows the step 2 answers with the file each came from, and a `# repo:` line naming the project's directory. `# repo: none` means bs ran outside the repo, or `paths.repo` is unset.

## 5. Harness setup

### claude_code

- Workers need nothing in Claude's settings: `bs spawn` passes each its own `--settings` (cross-session messages, auto-mode rules for its workspace, the guards). The queen gets none, and Claude in bypass permission mode holds incoming messages: if the keeper runs the queen in bypass mode, offer to add `"crossSessionInbound": "accept"` to `~/.claude/settings.json`.
- Workers start in the keeper's default permission mode, and one at a permission prompt waits in its pane. Ask which mode they want; `"permissions": {"defaultMode": "auto"}` keeps workers moving with a check on each action.
- **Folder trust.** Claude asks once per folder whether to trust it (a repo's worktrees share its answer), and a worker started in an untrusted folder waits at that prompt. The first `bs queen` in the project's directory asks it; the keeper picks **Yes, I trust this folder**. The cursor starts on **No, exit**, so a bare Enter quits the queen.

### agy (staged)

bs refuses to start agy sessions until its probes land; `bs doctor` prints the refusal. agy loads bs's guard hook only in a folder on its trusted list, which bs reads and never writes: the keeper starts `agy` once in each workspace directory and answers its trust prompt.

**Check:** the keeper has answered the questions for their harness.

## 6. Verify

For each project, open a herdr workspace on its directory, labelled like the workers: `herdr workspace create --cwd <dir> --label "<project> · queen"` (it prints the new pane's id; `herdr pane run <pane> "<command>"` runs a command there). In that pane, run (`--project <name>` for a non-git project, nothing for a git one):

```sh
bs doctor [--project <name>]   # every line ok or warn; FAIL names what to fix
bs queen [--project <name>]    # starts the queen in this pane; answer the folder-trust prompt (step 5)
```

`bs doctor` ends with a line per project check (`repo`, `guards`, …); `project: none here` means it found no project, so pass `--project`.

Then, inside the queen:

```sh
bs epic "setup check"                                    # → <project>-N
bs bead new --parent <project>-N "say hello and stop"    # → <project>-N.1
bs spawn --label "setup check" <project>-N.1
```

The worker opens in a new herdr workspace, reads its brief and replies; `bs send <worker> "reply to me with bs send: pong"` prompts it if it doesn't. The queen is woken by a `[bs] 1 unread` doorbell with nobody typing, and `bs inbox` shows the reply. **Check:** `bs doctor` passed, and the queen received the worker's reply through a doorbell.

The setup bead changes nothing, so a `github_pr` worker opens no PR. Clean up: `bs end <worker>` (a `git_worktree` worker's branch is kept; `bs prune` deletes it once merged, and `git branch -D <worker>` drops this unmerged one), then `bd close <project>-N.1 <project>-N`. **Check:** `herdr workspace list` no longer shows the worker's workspace, and both beads are closed.

Tell the keeper setup is done, and point them at **Run a queen and workers** in [`SETUP.md`](SETUP.md).
