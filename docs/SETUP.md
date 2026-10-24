# Setting up bs

Linux or macOS. Needs Go, [Claude Code](https://code.claude.com) with cross-session messaging (2.1.296 or later) or optionally [`agy`](#agy-workers) 1.4.6 or later for agy workers, [herdr](https://herdr.dev) for spawning workers, and `bd` 1.1.0 or later for beads, with one local store shared by every repo (`BEADS_DIR`; the skill below sets it up).

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
bs spawn --goal pr --label "ring on send" <project>-2.1  # worker <project>-2-1 in its own herdr workspace and worktree (--harness <kind> overrides per spawn)
bs send <project>-2-1 "…"      # store, then ring it awake; --now interrupts its current turn
bs ring <project>-2-1          # ring an idle agy worker whose earlier ring was held while busy or at a menu
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

### agy workers

What bs decided for agy, and why, is [DECISIONS.md R60](DECISIONS.md#configuration) (layer rule, mode, config, version drift) and [ADR-0011 Amendment 1](adr/0011-ring-by-push-where-the-harness-allows.md#amendment-1-agy-push-by-typing-idle-only-bees-1397) (the ring). This section is how to set it up.

#### Setup now

| # | Step | Check |
|---|---|---|
| 1 | Install `agy` 1.4.6 or later and run it once in a shell to sign in. A herdr server started over ssh hands `SSH_CONNECTION`, `SSH_CLIENT` and `SSH_TTY` to every pane, and agy reads them as a remote session that is "not signed in"; bs unsets them when it starts agy (bees-13.13.13). | `agy models` answers in a pane |
| 2 | Pick where agy runs. One worker: `bs spawn --harness agy …` (bees-13.13.12), the way bees runs it today. Every session: `harness = "agy"` in the global `~/.config/bees/config.toml`; it is machine-wide and moves `bs queen` too, and a queen on agy is untested (bees-13.13.11). | `bs config` |
| 3 | Model, per project or global: `[worker.agy] model` (and `[queen.agy] model`) must be a name `agy models` prints; bs passes it unchecked, and agy warns "not recognized" and picks its own for any other (`argon-high` is one, bees-13.13.22). agy can run Claude models too (`claude-sonnet-5-5-low`), but only on API billing, not a Claude subscription. | no "not recognized" warning when the worker starts |
| 4 | Approve commands one by one at agy's prompt in the worker's herdr pane, and choose agy's "always" answer only for the ones you trust (`bs`, `bd`, `go`, `git`, `gh pr create` are the usual ones). bs suggests no allow list: an "always" lands in `permissions.allow` in `~/.gemini/antigravity-cli/settings.json` (on agy 1.4.6), and that list is yours. The one entry bs's own tooling checks for is `command(bs)` (the live-test skill warns without it). | the next worker runs it unasked |
| 5 | Know what stays a prompt. agy matches an allow rule as a literal prefix and checks each segment of a chained command (`&&`, `;`, `\|`) on its own, so `cd <dir> && git status` asks about `cd`. Keep `rm`, `python3`, `sqlite3` and bare `gh` (`gh api`, repo admin) as prompts. `bs guard` runs before every command and its deny wins over any allow: a push to `main`, force-push, `gh pr merge` and `gh pr review --approve` stay denied with `command(git)` allowed. | — |
| 6 | Answer prompts in the worker's pane: the keeper, or a queen following the prompt policy in bees-13.13 (approve routine commands once or per conversation, never persist; deny `--as`). Denying one cancels the worker's turn. Never send raw keys such as `Escape` into an agy pane: it can cancel a prompt behind an overlay. | — |
| 7 | Check `main` is protected on GitHub (a ruleset or branch protection requiring a pull request, no force-push). bees' is, by its `main` ruleset; `bs guard` is the speed bump, protection the guard. | a direct push to `main` is refused |
| 8 | Run `bs doctor` and expect the lines below. With the global harness still `claude_code` (agy per spawn), only `agy version` and `agy mode` print; `harness agy`, `agy sign-in` and `agy ring` need `harness = "agy"`. | — |

| `bs doctor` line | Means |
|---|---|
| `ok harness agy: <path> <version>` | agy is on `PATH`, at 1.4.6 or later (`FAIL` if older). |
| `ok agy sign-in` | `agy models` answers with the three `SSH_*` unset, as workers run it. |
| `ok agy ring` | agy can't be pushed a doorbell, so bs types it into an idle agy's empty input box; points at ADR-0011's watch list. |
| `ok` / `WARN agy version` | `agy --version` against `agy.Probed` (1.4.6). agy updates itself (1.4.4 → 1.4.7 in one day), so a `WARN` naming what to re-probe (`.claude/skills/live-test/agy-probe.sh`) is expected after an update; `bs spawn` prints it too (bees-13.13.15). |
| `ok` / `WARN agy mode` | the newest bs-started agy's log (`~/.gemini/antigravity-cli/log/`) shows `--mode accept-edits` took, not a fallback to asking about everything. |
| `ok` / `WARN` / `FAIL herdr session <s>` | the herdr server runs and speaks the client's protocol (`FAIL` if not); `WARN` when the server is older than the client or herdr says it needs a restart: `herdr server stop` and start it again when no session you need runs. |

A ring held because the worker was busy, at a menu or being typed into says why on `bs send`, and `bs status` then flags it `stuck:`; the queen runs `bs ring <worker>` once it is idle.

#### Setup later

| Change | When | What it takes first |
|---|---|---|
| `--mode auto` with agy's terminal sandbox, so workers run unattended with agy's reviewer judging each command that leaves the sandbox | Not yet (R60); revisit in bees-13.13.26 when agy quota returns (~2026-10-28) or Google answers [antigravity-cli#1204](https://github.com/google-antigravity/antigravity-cli/issues/1204) | The keeper installs a per-binary AppArmor profile for agy (`/etc/apparmor.d/agy`, `flags=(unconfined) { userns, }`, drafted in bees-13.13.25), never `kernel.apparmor_restrict_unprivileged_userns = 0` machine-wide. Then probe that agy's sandbox lets `git push` and `gh pr create` out (it has no network, so they must leave it and be approved), since a sandbox that blocks them breaks the PR handoff. On macOS the sandbox is Seatbelt and needs no host change. |
| `--dangerously-skip-permissions` | Off; bs never passes it | (a) a probe shows `PreToolUse` hooks, so `bs guard`, still run and deny under it (the evidence in bees-13.13.25 says they do, unprobed here), and `bs guard` becomes deny-by-default on every tool, since the flag removes the only gate that judges a command; (b) `main` is protected (bees' is); (c) workers get a fine-grained GitHub token scoped to the repo (today's has `repo` and `admin:public_key`); (d) the beads store is backed up. |
| A queen on agy | bees-13.13.11 | A queen's held ring has no `Stop` hook to show it yet: it waits for `bs ring` or her next `bs inbox`. |

#### Future agy work that would simplify bs

The first two rows and the last are [ADR-0011's watch list](adr/0011-ring-by-push-where-the-harness-allows.md#revisit-when); the sandbox row is bees-13.13.26's, and the `Stop` row is bs's own wish. Re-probe when one ships.

| Upstream change | What bs deletes or unifies |
|---|---|
| agy wakes an idle session on a pushed message ([antigravity-cli#1022](https://github.com/google-antigravity/antigravity-cli/issues/1022)) | `Ring(agy)` becomes a push like Claude's: the screen reading (`Box`, `Ready`, `Menu`), and `bs ring` and `stuck:` for agy, go. |
| A local, documented API to prompt a live session that reports failure (`remote-control` reaches Google's servers only today) | The same, through a checked call instead of typing. |
| agy ships an AppArmor profile, or a sandbox that works on stock Ubuntu | `--mode auto` with no keeper setup; `mode.go`'s fallback check stays as the guard. |
| `Stop` hooks on cancelled turns, and richer `Stop` payloads | A turn cancelled at a prompt still shows its unread mail, so fewer rings wait for `bs ring`. |
| herdr refuses `agent prompt` while a permission menu is on screen | Closes the menu race whatever agy does; `Menu` goes from `Ring(agy)`. |

## Who bs takes you for

bs acts as its nearest ancestor process a session's entry recorded (a queen, a worker), else as the keeper; `bs status`'s first line says which. From the keeper's shell, `--as <session>` is a debug override (the keeper's escape valve) that acts as that session, with its permissions: `bs done --as <worker> ...`, `bs send --as queen-<project> <worker> "..."` while her pane is down. `--as` is refused (exit 2) under any recorded session, and `bs inbox --as <session>` only peeks, marking nothing read (bees-13.90).

## Other VCS: a command workspace and a custom goal

git with GitHub needs no config. Any other VCS (jj, hg, a cloud client with CLs) is two tables of your own argv in a config file ([ADR-0010](adr/0010-core-rebuild.md) Workspaces and Goals): `[workspace.command]`, which makes and removes each worker's directory, and a `[goal.<name>]` whose `cmd:` checks say whether the work was handed off and has landed. The queen then spawns with `--goal <name>`.

How bs runs them, the same for both tables:

- **argv, never a shell.** A `{key}` fills one whole argument from the spawn's record: `{dir}` (the worker's directory), `{name}` (the worker), `{bead}`, `{project}`, `{repo}` (the project's directory). A git_worktree also fills `{branch}`, `{base}` and `{start}`; a command workspace has none, and a check naming one is refused at spawn. A worker names nothing, so there is no `{pr}`: a check finds the change from the workspace. Need a pipe? `["sh", "-c", 'script using "$1"', "sh", "{name}"]` passes the value as an argument, never into the script.
- **Where.** `create` and `remove` run in the project's directory (`paths.repo`); `clean` and every `cmd:` check run in the worker's. The environment is only `PATH`, `HOME` and `LANG`, as `bs` has them; `bs spawn` refuses a command whose program isn't on that `PATH`, and `bs doctor` checks each.
- **Bounds.** Each runs within its `timeout` (default 60s); one that runs past it is killed with its process group.
- **Exit 0 is yes, any other exit is no.** A command that can't start or times out is neither: `bs done` exits 6. Write each check so that "nothing found" exits non-zero: `jj log -r <revset>` and `hg log -r <revset>` exit 0 on an empty revset, and `! cmd` turns a network error into yes. Wrap them: `test -n "$(jj log --no-graph -r … -T commit_id)"`.
- **Frozen.** A spawn records each check's argv, `clean`, `remove`, and the sha256 of the goal's `fragment` file: `bs done`, `bs end` and `bs prune` run what was recorded, never the config as it is now. A resume refuses a fragment changed since; `bs goal <worker> <goal>` records the new one.
- **The workspace.** `bs spawn` refuses a `[workspace.command]` with no `clean` (exit 0 when nothing in the workspace would be lost: it is what lets prune remove one), and a `dir` that already exists with no spawn recorded on it. A `create` that fails, times out, or makes no `dir` runs `remove`, and nothing is recorded. `bs end` keeps the directory; `bs prune` runs `clean`, then `remove`, only once the goal's frozen prune rule holds or the keeper discarded it, only on the directory `create` made (not one made there again since), and checks that `remove` left no `dir`. bs looks at nothing inside a command workspace: `clean` is the only guard on what's in it, so make it count ignored files too, where reports end up (an ignored `scratch/` is where five reports were lost, bees-13.7). A second spawn of the same worker waits for the first to record its directory, so two never make or remove it at once. Guards: when the project's directory is a git checkout, a worker is denied pushes to its default branch and force pushes; in any directory, the review host's merge and approve (a `[review.command]` host's `merge_deny` and `approve_deny`) and `[guards] deny`. `bs spawn` says which. Claude Code would ask to trust each command workspace's directory, a new folder per worker, so a Claude worker's spawn marks that one folder trusted in `~/.claude.json` (`projects[<dir>].hasTrustDialogAccepted`, under Claude's own lock) and prune drops its entry; a `dir` inside a git checkout is left to the repo's trust (bees-13.86).

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

### A review host of your own: `[review.command]`

`--goal pr`, `push=` and `review=` read changes through a review host. GitHub is built in; any other (a CL host) is `review.host = "command"` and a `[review.command]` table of your own argv, each printing one JSON value ([ADR-0010](adr/0010-core-rebuild.md) Amendment 6). bs ships no adapter for any tool: wrap yours in a script that prints these shapes. They run as above (argv, never a shell; only `PATH`, `HOME` and `LANG`; `timeout`, default 60s), in the project's directory. Unknown keys are ignored; absent, `""` or `null` is none. **Exit non-zero, or print anything but the JSON, when you can't answer**: bs reads that as "couldn't ask" (exit 6), never as no.

```toml
[review]
host     = "command"
fragment = "~/.config/bees/fragments/cl-host.md"   # the host's only prompt section: how to upload, and how the queen lands

[review.command]
url          = 'https://cl\.example/(?P<repo>[^/]+)/(?P<number>[0-9]+)'   # matched whole
repo         = ["cl", "repo", "--json"]                       # {"repo": "<name>", "protected": true}; null: no repo
changes      = ["cl", "list", "--client", "{dir}", "--json"]  # [change, ...] uploaded from the workspace, any state
view         = ["cl", "show", "{url}", "--json"]              # change, or null: no such change
ci           = ["cl", "checks", "{url}", "--json"]            # {"checks": [{"name": "test", "bucket": "pass"}], "required": ["test"]}
post_review  = ["cl", "comment", "{url}", "--file", "{body_file}", "--dedupe", "{marker}", "--json"]  # {"url": "<the comment's>"}
fetch        = ["cl", "fetch", "{url}"]                       # optional: make {commit} present locally
create       = "cl upload"                                    # shown to a worker with no change yet
retarget     = "cl rebase --change {number} --onto {base}"    # shown when a stacked change's target landed
push_hint    = "cl upload --change {number}"                  # shown when the host has an older head
merge_deny   = ["cl submit*", "cl * submit*"]                 # guard globs: the worker never submits
approve_deny = ["cl approve*", "cl * approve*"]               # ... nor approves
timeout      = "60s"
```

A **change** is `{"url", "state", "draft", "head", "head_commit", "base", "commits", "foreign", "foreign_repo", "editable", "created", "behind"}`: `url` and `state` (`open`, `merged` or `closed`) always, `created` (RFC 3339) in `changes`, so bs keeps only those since the spawn; `behind` is how many commits its base has that it lacks. Its repo and number come from its URL through `url`. `bucket` is `pass`, `pending` or `skipping`; anything else is a failure. `required` names the checks the change's base requires: one not reported yet is pending.

- **Placeholders.** `changes`: `{dir}`, `{repo}`, `{branch}`, `{base}`; `view` and `ci`: `{url}`, `{repo}`; `post_review`: `{url}`, `{body_file}` (the report, a temp file), `{marker}`; `fetch`: `{url}`, `{commit}`. Hints fill `{url}`, `{number}`, `{base}`, `{dir}`, `{branch}`, `{head}`.
- **`post_review` dedupes**: a comment on the change already carrying `{marker}` is a retry after a lost answer, so print that one's URL and post nothing.
- **One open change per workspace.** Two open changes from one workspace refuse the handoff, naming both, until a CL-chain rule lands.
- `bs doctor` checks each argv[0], runs `repo`, and fails a command host with no `review.fragment`.

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
