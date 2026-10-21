# bees keeps the mechanics; prompts carry the policy

> Supersedes S4 (Claude Code only, behind `{Launch, Ring, Alive}`). Amends [ADR-0004](0004-liveness-is-derived-not-stored.md), [ADR-0006](0006-spawn-and-naming.md), [ADR-0007](0007-claudes-registry-confirms-a-launch.md) and [ADR-0008](0008-config-files-one-tagged-struct.md). Epic bees-12; security criteria from bees-12.2. Settled with the keeper over three plannotator rounds and chat, 2026-10-21; [amendment 1](#amendment-1-pane-identity-bs-done-and-the-background-work-gate) (bees-12.16) the same day.

bees-12's north star (the same as bees-11's): a new keeper on another harness (Gemini's agy, oh-my-pi) and another VCS (a CitC client and CLs instead of a git worktree and PRs) can set bees up with agentic help, and the config shows every decision about what runs where. The fact finding (queen-bees, 2026-10-21) found Claude Code wired into about 12 places with no interface, git in identity, spawn and prune, and 40 mentions of PRs, merges, pushes, branches and prune in `QUEEN.md`/`WORKER.md` (`grep -oiE` on main). S4, ADR-0006 and ADR-0008 each assumed one of those three things.

**The rule** (keeper, round 3): a worker is a capable session. bs does only what an agent can't do reliably from inside its own session: start and find sessions, keep the mail, know who is talking, keep the guards, and refuse to end a session that still runs background work. Everything that is judgement goes into the prompt text for the configured kind: what "done" means, whether work landed, how to make an unusual workspace.

```toml
harness = "claude_code"   # top level, global file only: every queen and worker on the machine

[workspace]
kind = "git_worktree"     # git_worktree | none (opens on paths.repo)

[delivery]
kind = "github_pr"        # github_pr | manual: picks the role prompts' delivery fragment
fragment = ""             # optional: a file of your own wording instead (a CL flow)

[guards]
deny = []                 # empty: the delivery kind's default; each harness renders them
```

- **The core** is the logbook (ADR-0001, 0002, 0005), the role prompts, the bead → name → session binding (ADR-0006), spawn, end and prune, messaging, and liveness and identity from herdr. It names no harness.
- **Fixed, not adapters:** herdr (it already starts 24 agent kinds, reports each one's agent in its pane, and opens a workspace without git) and bd (personal tracking, kept local: keeper, round 1).
- **One port, the Harness,** with one package per harness and a stateful fake that encodes the real tool's constraints (motif 13). Workspace is a two-value config switch inside the core, and delivery is a prompt fragment, not a port. Today's behaviour is `claude_code`, `git_worktree` and `github_pr`, with no change for a keeper who sets nothing.

## Harness

The harness is the agent program in the pane: how it starts, how it is rung, and how it spells guards. Where a session runs, and which session bs runs in, are herdr's to answer for every harness. SPEC's `{Launch, Ring, Alive}` was close: `Alive` moved into the core, and a launch is the kind herdr starts plus the files and argv bs prepares.

```go
package harness

type Priority string

const (
	Now  Priority = "now"  // interrupt the current turn
	Next Priority = "next" // after the current turn
)

// Start is everything the core decides about one launch. Adapters render it; they
// never choose a name, a directory or a prompt.
type Start struct {
	Role       prompt.Role       // Queen | Worker
	Name       string            // session name (ADR-0006)
	Dir        string            // the workspace directory
	PromptFile string            // role prompt + BEADS.md + the delivery fragment
	Doorbell   string            // first user message; "" for the queen
	Continue   bool              // resume the last conversation in Dir
	Guards     guards.Rules      // deny patterns and allow intents (prose with the branch and repo), built by the core
}

// Launch is what Prepare returns. Prepare is pure: the core writes Files (0600, under the
// cache dir, each named <name>-<key>-*), so the launch-length check runs before anything is
// made, and cleanup is the forward glob <name>-[a-z]*-* (motif 3). bs queen execs the binary
// named Kind(): it is the same word for claude, omp and agy.
type Launch struct {
	Args   []string          // after `herdr agent start … --`; file paths as {{key}}, filled by the core
	Files  map[string]string // key → content: claude's settings JSON; omp's overlay and extension
	Record map[string]string // facts known before the start that a later start needs (omp: its profile)
}

// Target is a Live session as the core found it, from the logbook's record and herdr.
type Target struct {
	Name, Pane string
	PID        int // the agent's pid, from herdr pane process-info
}

// harness.For(name) maps a recorded harness name (claude_code) to its adapter, one forward
// (built as harnessFor in cmd/bs: package harness can't import the adapters that import it)
// function. Kind and Ring need no config table, so bs send and bs status build it without
// reading the config files (ADR-0008's skip list); Prepare also needs the role's table
// (claude.New(cfg.Worker.ClaudeCode)).
type Harness interface {
	Kind() string                                 // herdr --kind: claude | omp | agy; also the harness process's name in the pane
	Prepare(st Start) (Launch, error)             // pure: render guards, files and argv
	Ring(t Target, text string, p Priority) error // the doorbell
}
```

Cleanup is not a method: the core removes `<name>-[a-z]*-*` from the cache dir on `bs end` and before each start. A `Files` key is lowercase and starts with a letter (`settings.json` is written as `<name>-settings-*.json`), so the glob never takes the files of a worker whose name extends this one, nor a prompt file (`<project>-worker.md`): `bees-12-*` would take `bees-12-3`'s (bees-12.6). There is no `Start.Env`: bs pins no env, and a session finds its project and name from its herdr pane (amendment 1).

**Liveness and identity come from the recorded harness process, for every harness** (keeper; amendment 1). Every session bs starts runs in a herdr pane it records: a worker's in its `spawned` entry, the queen's in a new `queen` entry that `bs queen` writes from `HERDR_PANE_ID` (herdr sets it in every pane) before it execs the harness. With the pane, bs records the harness process: pid, start time (`proc.Start`) and executable, a worker's in its `started` entry, the queen's in her `queen` entry (`exec` keeps her pid). `bs queen` outside a herdr pane refuses with exit 2 (a queen bs can't see is a queen no worker can ring), and so does a second `bs queen` while the newest `queen` entry's process is alive. bs never reads herdr's `agent`, `agent_status` or busy/idle.

- **Start confirmation.** While `herdr agent start` waits, bs polls `herdr pane process-info --pane <pane>` for the foreground process whose name is `Kind()` (if only the shell shows, it walks down from `shell_pid` to the harness executable). Found within ADR-0007's 20 s wait: bs writes the `started` entry and the start is confirmed. Not found: unconfirmed, and bs names the pane to look at. A herdr error is an error (motif 7).
- **State** is *Live* while the recorded process is alive with its recorded start time, else *Absent* (a message waits; spawn may start or restart). Nothing else: a worker held at a trust or permission prompt is Live and is rung like any other (the message is stored first, and the harness shows it when the prompt clears); another harness in the pane means the recorded process is gone, so Absent. A pid reused by another process fails the start-time check.
- **Naming.** Once a start is confirmed, bs names the agent in herdr (`herdr agent rename <pane> <name>`), for the sidebar and as a launch fence: a second `herdr agent start` with a live agent's name fails with `agent_name_taken` and starts nothing (probed). bs never reads the name back.
- **Teardown of a moved worker.** A pane the keeper moves (`herdr pane move`) gets a new id, and a workspace whose last pane moved out closes (probed); the process keeps running. `bs end` finding the recorded workspace gone while the recorded process is alive refuses ("bees-12-3 still runs as pid 41700; close its pane, or bs end --force") rather than recording `ended` over a running worker (motif 12).
- **Identity** (`project.Me`) is `HERDR_PANE_ID` matched against the open entries (amendment 1's scan), accepted only while that entry's recorded process is alive and is an ancestor of the calling `bs`. So neither the keeper's shell left in a pane nor a later harness started there is the session, and a moved worker, which keeps its old `HERDR_PANE_ID`, still is. A worker whose first `bs inbox` runs before its `started` entry lands waits for it, up to the 20 s confirm. Identity needs no herdr call, so it works while herdr is down; spawn's checks fail closed then (T5). Inside a recorded pane, an `--as` naming anyone else is refused, the same guard as today's ancestor walk. Outside a recorded pane, `--as` and `BS_SESSION` apply as now.
- **Outcomes:** a start is started or unconfirmed; a ring is rung (written, not proven read), not running, unsupported (`ErrNoRing`: agy until its ring is proven) or failed, and none fails the send; a resume continues or starts fresh, and a harness that can't tell must fail visibly. Busy/idle (R38) stays out.

The probe (herdr 0.9.5, Claude Code 2.1.306, a throwaway workspace per harness on a scratch dir, 2026-10-21): each harness was started bare in the root pane with `herdr pane run`, and `herdr pane process-info --pane` listed it as the pane's only foreground process, with its own process group, under the pane's shell:

| | claude | omp | agy |
|---|---|---|---|
| `foreground_processes[0]` | `claude`, pid 3230153 | `omp`, pid 3230149 | `agy`, pid 3230150 |
| `/proc/<pid>/exe` | `~/.local/share/claude/versions/2.1.306` | `~/.local/bin/omp` | `~/.local/bin/agy` |
| `shell_pid` (its parent) | 3229429 | 3229436 | 3229443 |

So nothing is built for a harness without a registry, and the walk down from `shell_pid` is a fallback none of the three needs. Claude's own registry stays, read only inside `claude_code`'s `Ring`: the `<pid>.json` for the recorded pid gives the socket and the token.

What `claude_code` does, and the code that moves behind it (bees-12.6):

| Method | claude_code | Today in |
|---|---|---|
| `Kind` | `claude` | `HerdrCLI.StartClaude` (now `StartAgent(kind)`) |
| `Prepare` | `--settings` file (crossSessionInbound, autoMode, permissions.deny), `--name`, `--append-system-prompt-file`, `--model=`, `--continue` | `worker.workerSettings`, `Fleet.launch`, `cmd/bs/queen.go` |
| `Ring` | `~/.claude/sessions/<pid>.json` → socket and `.key` token, then NDJSON | `claude.Ring`, `claude.Find`, `bs send` |
| (cleanup, core) | `<name>-[a-z]*-*` in the cache dir | `bs end`, `Fleet.start` |
| (to the core: the recorded process) | liveness and identity | `claude.List`, `ClaudeSessions`, `bs status`, `project.registered` |
| (dropped) | the untrusted-repo warning: a worker at its trust prompt is Live, and the keeper answers it in its pane | `claude.Trusted`, `bs spawn` |

**Ringing a harness with no ring.** omp's ring is a bs extension listening on `$XDG_RUNTIME_DIR/bees/<name>.sock` (a path built from the name, not looked up), with a token file beside it (SEC-1). agy has no proven ring (bees-12.7): until one is, an agy worker reads mail only when it next runs `bs inbox`, and `bs send` says it couldn't ring.

**No env pinning** (amendment 1). bs sets no `BS_*` variable in a session's env, so no harness needs a native pin, and every `git_worktree` worker, whatever the harness, opens with `herdr worktree create` and nests under its repo. bs still starts agents only in a workspace's root pane.

**Adapter requirements** (from the probes; bees-12.8 and any later adapter meet them):

- **Resume.** The core sets `Continue` only when the worker has read a message since this spawn (ADR-0007). `Prepare` must then resume that conversation or fail visibly, never start a fresh one silently or pick an older spawn's transcript. omp's `--continue` takes the newest session in the directory, and nothing says it exits when there is none: probe it. agy's `-c` never reports "none" and the conversation id exists only after agy starts, so a pure `Prepare` can't return it: how the agy adapter finds the id to resume (an id bs chooses at start, if agy takes one, or one read back after the start) is bees-12.12's (agy harness adapter) to probe. A resumed session keeps its recorded system prompt.
- **Ring priority.** `Now` interrupts the turn and `Next` waits for it (omp: steer and followUp). No mapping may fold a doorbell into context without a turn (omp's aside in plan mode).
- **The prompt file** is checked readable just before launch, by the core: omp silently falls back to no prompt. agy has no system-prompt file flag; a custom agent via `--agent` or a SessionStart `systemMessage` are the untested candidates.
- **Leftovers outside the cache dir** (omp's transcripts, `~/.omp/logs/http-*`) are the harness's own state. A new spawn never continues them, since `Continue` needs a receipt since that spawn.
- **Shared harness state** across concurrent workers (omp's profile databases) is unprobed; bees-12.8 probes it before claiming more than one worker.
- Never a flag that skips the harness's own permission checks (agy's `--dangerously-skip-permissions`).
- **Guards must be loaded, or the start refused.** agy loads its hooks (where its guards live) only in a folder it trusted at startup, so agy's `Prepare` returns an error for a folder missing from agy's trusted list (read only; bs never writes it): the keeper trusts it once by hand, then spawns again.
- **The harness process exists before the start returns.** In the first probe, a Claude busy on its first prompt was in its pane within 3 s, while `herdr agent start` timed out at 10 s (ADR-0007's case), so confirmation polls `process-info` rather than waiting on the start's answer.

**The harness key** (amends ADR-0008's "no selector until a second harness"):

- `harness` is a top-level key, global file only, enum of the built-in adapters, default `claude_code`. It names the harness new sessions start on, and one machine runs one (keeper, 2026-10-21).
- Only the active harness's tables are valid. `[worker.oh_my_pi]` with `harness = "claude_code"` is an error naming the file, the table and the `harness` key.
- Each `spawned` and `queen` entry records the harness. Ringing and teardown use the recorded harness, whatever the config says now (motif 12), and the skip-list commands (ADR-0008) know it without reading the files.
- `bs queen` and `bs spawn` refuse (exit 2) when the project has a live session recorded under another harness, naming the sessions to `bs end`. `bs end`, `send`, `inbox` and `status` never refuse on a switch. Each project checks on its own next start; this check reads no other project's logbook.

## Workspace and delivery are config, not ports

**Workspace** is a switch inside the core, with no hooks (amendment 1):

- **`git_worktree`** (default) is today's path: branch `<name>`, directory `<paths.worktrees>/<name>`, orphans matched by herdr's `checkout_path`, `bs end` refusing a dirty worktree without `--force`, `bs prune` deleting merged branches (R42).
- **`none`** opens a herdr workspace on `paths.repo` (`herdr workspace create --cwd`, after checking the directory exists: a missing `--cwd` exits 0 and opens `$HOME`). bs makes and removes no directory: the keeper makes it (a CitC client) and points `paths.repo` at it, and `bs end` only closes the workspace. The queen decides how many workers share an unhooked checkout. Under `none`, `paths.repo` is a location, not one link of the project's repo lookup: it beats the git checkout bs runs in (ADR-0008's chain), is taken as given (any directory, a linked worktree too; so is `--repo`), and `bs spawn` refuses when neither it nor `--repo` names the directory, rather than open workers in the queen's own checkout, where one of them moved it off main (bees-12.28). `bs config` says which directory workers share, or that none start. bs tags the workspace with its worker and path (`herdr workspace report-metadata <id> --token worker=<name> --token path=<dir> --source bs`; herdr 0.9.5 refuses `--source` before `--token`), since `workspace list` shows no directory for a plain workspace. herdr 0.9.5 cuts a token's value to 80 characters without a word (probed, bees-12.5; bees-12.17), so the path token is for people, and bs matches a workspace to its worker, for `bs end` and the orphan check, by the `worker` token, never by a path read back. An untagged one is *unknown*, reported and never removed.

**Sidebar labels** (bees-12.14, keeper): `herdr workspace create` opens a workspace herdr's sidebar shows with no repo above it, unlike `herdr worktree create`, which nests a worktree under its repo. `herdr worktree open` would nest an existing git checkout, but `none` exists for checkouts that aren't git worktrees, so bs keeps `workspace create` there and shows the project in every label instead: herdr's label is `<project> · <label>` for every workspace kind and harness. bs opens only workers' workspaces; the queen runs in one the keeper opens, so the tools that open one for her (the live-test skill, the bake-off's `mkrun.sh`) label it `<project> · queen` (or `· lead`), and `docs/SETUP.md` suggests the same. The queen still writes only the words, the label rules and 32-character limit apply to them, and the logbook and `bs status`'s LABEL column keep only the words: one forward mapping, never parsed back (motif 3). Workspaces already open keep their old label until respawned.

The `spawned` entry records the directory, and every later step uses the record, never a dir rebuilt from config (motif 12). `bs end` refuses before it closes anything: on a dirty worktree (`git_worktree` only) and on background work (amendment 1), both without `--force`.

**The workspace fragment** (bees-12.28, bees-12.29) is to workspace what the delivery fragment is to delivery: `internal/prompt/workspace/<kind>.md`, assembled under `## Workspace` with the same per-role parts, from the spawn's recorded kind (the queen's from config). It holds every line about where the work lives: the branch, the worktree, `bs prune` under `git_worktree`; a shared directory with no branch, no worktree and no `git init`, and a done report that lists files, under `none`. `QUEEN.md`, `WORKER.md` and the delivery fragments point to it and name no branch, so a `none` worker never hears "commit on your own branch". It is built in only: no user file, since the kind is a two-value switch. `none` with `github_pr` (the default delivery) is refused by `bs queen` and `bs spawn`: a PR needs the worker's own branch; `manual` or a `fragment` works.

**Delivery** sets three things, none a port: the role prompts' delivery fragment, the guards' default, and which proofs `bs prune` uses. Under `none` there are no branches, so `bs prune` has nothing to do. The fragment is `internal/prompt/delivery/<kind>.md`, assembled like `BEADS.md`:

- **`github_pr`** (default) is today's PR wording, moved word for word out of `QUEEN.md` and `WORKER.md`, so prompts don't change for today's users (bees-12.3).
- **`manual`**: the worker reports with `bs done --report`, the keeper closes the bead. `bs prune` uses only its git proofs (ancestry, identical paths), never GitHub.
- **`fragment`** names a file of the user's own wording, for a CL flow or anything else; the queen judges when such work has landed.

**Guards** stay in config (keeper, round 3). `[guards] deny` holds harness-neutral shell-glob patterns over a command line. Empty means the delivery kind's default: for `github_pr`, today's push and merge matrix against the repo's real default branch (bees-11.7); for `manual`, no push to the default branch. Each harness renders them in `Prepare`: Claude's `permissions.deny` and autoMode allow prose, omp's `bash.patterns` in a per-start `--config` overlay, agy's PreToolUse hook. They are a speed bump; branch protection, or the VCS's own review gate, is the real guard.

## Naming (amends ADR-0006)

The name is still one forward function of the bead (`bees-12.3` → `bees-12-3`) and is the session name, the herdr agent name and the message address. It is the git branch and the `-WT` worktree directory only under `git_worktree`. Whatever a spawn made is recorded in its `spawned` entry, and teardown uses that record, never a name rebuilt from config (motifs 3, 12). A session's project comes from its pane's entry (amendment 1); a process the keeper starts by hand uses `--project`/`BS_PROJECT`, else git, else `paths.repo`.

## Amendment 1: pane identity, bs done and the background-work gate

Keeper decisions, 2026-10-21 (bees-12.16), after the stage beads were filed. They change the text above in place; this section holds the detail.

**One mechanism.** The harness process bs records at start (pid, start time, executable) is the single fact behind liveness, identity and the closure gate, for every harness. One `internal/proc` function, on Linux (`/proc`) and macOS (`/bin/ps`, R47), answers both questions bs asks of it: is this process alive with this start time, and what are its descendants.

| question | answered by | used by |
|---|---|---|
| alive? | the recorded pid alive with its recorded start time | `bs status`, the twin check, ringing, `bs end` |
| who is this? | `HERDR_PANE_ID` → the entry that recorded the pane, accepted only while its process is alive and an ancestor of the calling `bs` | `project.Me`, `send` (any project's logbook; the sender stays the pane's), `inbox`, `done`, the `--as` guard |
| still working? | the process's descendants not in the baseline taken at start | `bs done`, `bs end` |
| started? | herdr's `process-info` shows the harness process in the pane within the 20 s wait | `bs spawn` (ADR-0007) |

herdr is asked only to open, start in, name and close panes, and for `process-info` at start; its `agent`, `agent_status` and busy/idle are never read.

**1. Identity comes from the pane; bs pins no env.** A session's identity is `HERDR_PANE_ID` (herdr sets it in every pane) → the entry that recorded that pane → its project and name. The logbook directory is the global file's `paths.logbook_dir`, so every process reads the same one.

```text
pane = $HERDR_PANE_ID                    # unset: not a session bs started
dir  = global config paths.logbook_dir   # never the project file, never env
for db in dir/*.db, opened read only (no DDL, no WAL switch, no migration):
    candidates: open spawned entries with data.pane = pane,
                and the newest queen entry if its pane = pane
    any db that can't be opened or read → error naming the file (fail closed, SEC-2)
keep the candidates whose recorded process (worker: newest started entry; queen: her entry)
    is alive with its start time and is an ancestor of this bs
one  → its project and name
none → not a recorded pane: --project, BS_PROJECT, git, paths.repo, and --as, BS_SESSION, as before
two+ → error naming both (motif 4)
```

- **The scan, not an index.** A machine holds tens of logbooks, each opened read only in about a millisecond. An index would be a second truth (motif 1); if the scan ever gets slow, one can be added as a rebuildable projection, as ADR-0002 allows for unread. The migration's copy aside is named `<project>.db.pre-<version>`, so the scan never reads it.
- **`paths.logbook_dir` becomes global only** (amends ADR-0008): a project file's value would put that project's logbook where no pane scan looks.
- **Inside a recorded pane the pane decides.** An inherited `BS_LOGBOOK`, `BS_LOGBOOK_DIR`, `BS_PROJECT` or `BS_SESSION` (herdr's server env, a keeper's shell) is ignored, with a warning when it disagrees: that is what the pins used to guard. The sender is always the pane's session, in the pane's project. `bs send --project dotfiles queen-dotfiles` from queen-bees' pane still works, as cross-project mail does today: `--project` picks only the recipient's logbook, the message's actor is the pane's name (an `--as` naming anyone else is refused), and its data records `from_project` (here `bees`), so `bs inbox` shows where to reply (`bs send --project bees queen-bees`) without reading the project back out of a name (motif 3). `inbox` and `done` act only in the pane's own project and refuse a `--project` naming another. Read-only commands (`bead ls`, `status`) may read another project.
- **Those variables stay for processes the keeper starts by hand** outside a recorded pane: tests, scripts, a one-off `bs send`, a copied logbook. `bs queen` and `bs spawn` refuse (exit 2) when the logbook they resolve is not `<global logbook_dir>/<project>.db`, since the fleet's panes would never find it. `bs queen` keeps dropping inherited `BS_*` from the env it execs with.
- **The skip list** (ADR-0008: `send`, `inbox`, `status`, `bead`, `epic`, `version`, now also `done`) was keyed on `BS_LOGBOOK_DIR`, which sessions no longer carry. With `HERDR_PANE_ID` set, those commands read only the global file's `paths.logbook_dir`, and skip the project file once the pane is found. A global file they can't read falls back, with a warning, to the default `~/.local/share/bees`: a pane found there is right, and a pane found nowhere is an error, never a new logbook. A pane not recorded anywhere reads the files as any command does.
- **No herdr call.** The scan needs only the env var, the logbooks and the process table, so it works while herdr is down. A pane with no live recorded process names no one, so a pane id reused after a herdr restart names no one (T4).

What bees-11.3 shipped that goes: the `env` block in a worker's `--settings` (`BS_PROJECT`, `BS_LOGBOOK_DIR`, `BS_LOGBOOK=""`, in `worker.workerSettings`), `Project.LogbookDir` and its spawn refusal for a `BS_LOGBOOK` not named `<project>.db` (replaced by the refusal above), and the skip list's `BS_LOGBOOK_DIR` trigger (replaced above). `bs config`'s note when `BS_LOGBOOK_DIR` is set stays. R46's `BS_PROJECT` pin goes too: a `--repo` worker's project is the one whose logbook recorded its pane. Never built, now dropped: `Start.Env`, the `BS_SESSION` and `BS_HARNESS` pins (bees-12.10), the `git worktree add` plus `workspace create --env` path for a harness with no native pin (bees-12.5), and omp's and agy's env file (bees-12.8, 12.12). Every `git_worktree` worker opens with `herdr worktree create` and nests; `none` opens with `workspace create --cwd` and no `--env`.

**2. `bs done` is a logbook entry.** `bs done --pr <url>` or `bs done --report <text>` (one is required; the delivery fragment says which) writes a `done` entry: actor the worker, subject its queen, ref its open `spawned` entry, data `{bead, pr}` or `{bead, report}`. It replaces the free-text `done: <bead> PR #N` message, and rings the queen as a send does. Unread widens from messages to messages and `done` entries addressed to me with no receipt from me (amends ADR-0002); a `done` stays unread after its worker has ended, unlike a message to a worker. `bs status` shows the newest `done` since the open spawn. A second `bs done` (an amended PR) is a new entry, and the newest wins. `bs done` refuses outside a recorded pane and while the gate below refuses. Closing the bead stays with the queen or keeper (motif 5).

**3. The background-work gate, for every harness.** The `started` entry (ref the `spawned` entry) that confirms a start (item 5) also holds the baseline: every descendant of the harness process as (pid, start time), read as soon as `process-info` first shows the process, so the doorbell's first tool calls are not yet running. Each start writes its own; the gate reads the newest since the open spawn. At `bs done` and `bs end`, a descendant of the recorded pid (alive with its recorded start time; no herdr call, so a moved pane still works) that isn't in the baseline, isn't the calling `bs` or one of its ancestors, and is still alive in a second read 1 s later (a hook or statusline command is gone by then) is background work. Both refuse (exit 5), listing each one's pid, command and any TCP port it listens on:

```text
bs end: bees-12-3 still runs background work; bs end --force ends it anyway
  pid 41822  python3 -m http.server 8000   listening :8000
  pid 41910  go test ./...
```

- Descendants are found by parent pid, not by the pane's tty: Claude runs each Bash call in a new session with no controlling tty (probed, Claude Code 2.1.306). Linux reads `/proc/*/stat`; macOS uses `/bin/ps` (R47). Ports come from `/proc/<pid>/fd` socket inodes matched to LISTEN rows in `/proc/net/tcp{,6}`, else `lsof` by absolute path (bees-6.10's plan).
- **Misses.** A process that detaches (`setsid` plus a double fork, `nohup … &` from a shell that exits) is reparented away and escapes the walk, and work started before the baseline read is in the baseline. The gate catches the common cases, a backgrounded tool call or a server the agent started; it is a speed bump, like the guards.
- **False alarms.** A helper the harness starts after the baseline (a lazily started MCP server) or a sibling tool call still running shows as new. The gate's stage bead probes each harness for helpers before shipping; the listing names them, so the queen can judge.
- **Bounded and fail closed.** The walk and the port read share one 5 s deadline (motif 15). Any error, including a live session with no `started` entry, refuses with its own exit code (6) and message, "bs can't check for background work: …", never the refusal above and never "none" (motif 7).
- **Overrides.** `bs end --force` overrides both refusals; the queen asks the keeper first (`QUEEN.md`). `bs done --force` writes the entry anyway with the pids it found in its data, so a false alarm never blocks a report (motif 5) and the queen sees what was running. `bs end --all` gates each worker on its own: it ends those that pass and lists the rest, as it does for a dirty worktree. The gate covers workers; the queen has none of their descendants.

A worker whose recorded process is already gone has nothing to walk (its children were reparented away): `bs end` says so and ends it. This supersedes bees-6.10's port-only check.

**4. No `init` or `finalize` hooks.** `none` opens on `paths.repo`, a directory the keeper made (a CitC client). Hooks come later, if ever; bees-12.5 builds `none` without them.

**5. Liveness is the recorded process** (keeper, after the probe under *Liveness and identity*). A session is Live while its recorded harness process is alive with its recorded start time, else Absent; there is no Starting, Blocked or Foreign state. A worker at a trust or permission prompt is Live, and another harness in the pane means the recorded process is gone. A start is confirmed when `process-info` finds the harness process within ADR-0007's wait. This drops what bees-12.10 planned to read from herdr: `herdr agent get`, `pane get`'s `agent` and `agent_status`, and the fake's lagging status.

**6. Delivery fragments shape only the wording:** PR or CL, `bs done --pr` or `--report`. The kinds, guards and prune proofs stay as above (bees-12.3).

## Security acceptance criteria (bees-12.2)

Every harness adapter, the claude_code extraction (bees-12.6) and the omp adapter (bees-12.8) included, meets these before it ships. Same-uid processes can't be fenced from each other (bees-8.8), so the bar is: nothing another user or another machine can reach, and nothing weaker than Claude Code's own path.

- **SEC-1, ring authentication.** The ring endpoint is a unix socket mode 0600 in a directory mode 0700 (no TCP, no abstract socket). A bs-owned endpoint (omp's extension) also requires a random per-session token of at least 128 bits, kept in a 0600 file beside the socket, and drops a connection without it. Claude Code (2.1.306, T1) meets the file-mode half and no more: it acted on a ring with no auth line and on one with a wrong token, though the session had a key file. Its boundary is the uid, no weaker than bees-8.8's limit, so `claude_code` ships as it is, and the gap is Claude's to close.
- **SEC-2, no stale liveness.** A session is Live only while the process its open entry recorded is alive with its recorded start time; a pane whose spawn has ended names no one. A herdr or process-lookup error is an error, never Absent (motif 7). claude_code's `Ring` reads only the `<pid>.json` for the recorded pid and checks its procStart. Today's `claude.List` fails open (`filepath.Glob` drops I/O errors, an unreadable file is skipped); it leaves the liveness path, and the `Ring` read must not repeat that.
- **SEC-3, repo hooks** (bees-12.2's third finding: omp auto-loads a repo's extensions and hooks). Dropped by the keeper, round 3: bees runs on the keeper's own repos, and a harness's own trust prompt waits in its pane for the keeper. omp still launches with `--trusted-extension <bs extension>`, which bees-12.8 needs anyway to load the ring extension alone.
- **SEC-4, no secrets in bees** (added; not one of bees-12.2's findings). API keys and OAuth tokens stay in the harness's own auth. bs never writes them to config, the logbook, an env var, a per-start file or herdr.

Test plan, run by the bead that builds each adapter:

| Id | Check | How |
|---|---|---|
| T1 | Claude drops a ring without the token, or doesn't | **done** (bees-12.1, 2026-10-21): NDJSON to this session's own socket, once with no auth line and once with a wrong token: both arrived as user turns. Re-run on a Claude upgrade |
| T2 | the bs-owned socket refuses no token and a wrong token | unit test against omp's extension listener; live-test with `socat` |
| T3 | modes 0700/0600 on the socket dir, socket and token file | unit test after `Prepare`, and a check in `bs doctor` (bees-12.9) |
| T4 | a reused pane or pid names no one | an ended spawn's pane now running the harness is not Live; a recorded pid reused with another start time is Absent; claude_code's `Ring` refuses a `<pid>.json` whose procStart doesn't match |
| T5 | herdr down fails closed | herdr socket unreachable: spawn and end refuse; `bs status`, `inbox` and `send` work from the recorded processes, and a ring that needs herdr says it couldn't ring |
| T7 | a repo config can't loosen the guards | toy repo whose project config allows `git push origin main`: the push is still denied |
| T8 | no secret reaches bees | after a live run, grep the logbook, config, cache dir, per-start files and `/proc/<pid>/environ` of the pane shell for the key's prefix: none |
| T9 | a pane names one session or none | logbooks in a temp dir: a stale open entry whose pane runs something else, the same pane in two logbooks, an unreadable `.db` (error), a `.db.pre-1` copy (ignored), herdr down with the recorded pid dead |
| T10 | the background-work gate | a fake process tree: a new child refuses with exit 5 and its port; a baseline child, the caller's chain and a 0.5 s child pass; no `started` entry or a walk error exits 6; `--force` passes |
| T11 | cross-project send keeps its sender | from a recorded queen-bees pane, `bs send --project dotfiles queen-dotfiles` writes to `dotfiles.db` with actor `queen-bees` and `from_project: bees`; queen-dotfiles' `bs inbox` shows the reply command; `--as` someone else is refused; `bs inbox --project dotfiles` and `bs done --project dotfiles` from that pane are refused |

T6 (a repo hook rewriting a worker's command) went with SEC-3.

## Considered Options

- **A harness per role** (`queen.harness`, `worker.harness`). Rejected by the keeper: one harness per machine is enough, and it keeps one ring path in a fleet.
- **A bs-owned registry, written by omp's extension or an agy SessionStart hook running `bs hello`, bound to the spawn entry.** Round 2's pick. Rejected once herdr proved to report each harness's agent in its recorded pane: a second truth to keep fresh (motif 1), with stale entries, a binding rule and a hook that loads only in a trusted folder.
- **A `bs hello` the model runs because its prompt says so.** Rejected: a prompt step is not a process fact (motif 9).
- **Claude's registry for Claude, herdr for the rest.** Rejected by the keeper: two liveness and identity paths.
- **Workspace and Delivery ports** (`Create/Remove/Dirty/List/Release` with a `command` kind running user scripts; `Landed/Guards/Prompt` with a `command` kind answering "landed?"). Rounds 1 and 2. Cut in round 3: judging a CL landed is prompt text for a capable worker, and what bs proves stays git-only (`git_worktree`, prune). Making and removing a workspace came back as two formal `init` and `finalize` hooks, then went in amendment 1.
- **`Preflight` refusing a launch without a trust step (SEC-3).** Cut in round 3 with SEC-3.
- **Moving every `git_worktree` worker to `git worktree add` plus `workspace create --env`,** for one env path. Rejected: it changes the default path and drops herdr's `checkout_path`. Amendment 1 drops the env path for every harness instead.
- **Pinning `BS_PROJECT`, `BS_LOGBOOK_DIR`, `BS_SESSION` and `BS_HARNESS` in each session's env** (the original `Start.Env`). Rejected in amendment 1: a harness with no native pin then needs `workspace create --env`, which doesn't nest, or a bs-written env file its extension or hook loads; the pane already names the session.
- **A port-only teardown check** (bees-6.10: refuse while a descendant holds a listening port). Superseded by the background-work gate, which also catches a build or a test run that holds no port.

## Consequences

- `internal/harness/claude` becomes the `claude_code` adapter behind `Harness`; the core packages stop importing it (bees-12.6). Existing tests pass unchanged.
- Liveness and identity move from Claude's registry to the harness process recorded at start, for every harness, Claude included: a behaviour change, so it lands as its own stage after the extraction (bees-12.10), with `bs status`, the twin check, start confirmation, `project.Me` and the `--as` guard re-live-tested. It amends ADR-0004 (liveness is the recorded process alive with its start time) and ADR-0007 (finding the harness process in the recorded pane within the wait confirms a start; herdr's `timeout` alone stays unconfirmed). `bs queen` needs a herdr pane.
- The `spawned` entry's data gains `harness` (the config name, `claude_code`; herdr's kind is derived from it, one forward mapping, motif 3) and the harness's `Record`. Old entries read as `claude_code` and `git_worktree`.
- New entry kinds `queen` (harness, pane, pid, start time; never closed: the newest one is the queen), `started` and `done` (amendment 1) need a logbook migration, one rebuild for all three. The `kind` CHECK constraint can't be altered in SQLite, so the table is rebuilt once inside `BEGIN IMMEDIATE` (the write lock every writer already takes), guarded by `PRAGMA user_version`, after copying the file aside (motif 14). An older shared `bs` keeps working on the rebuilt table: its reads select by kind, so they skip `queen`, `started` and `done` rows, and the new rows' actors are sessions it already lists, and its writes satisfy the wider CHECK. It ships as its own PR.
- `project.Me` needs the logbook, so `send` and `inbox` resolve identity after opening it; the launch-checkout check that read Claude's registry goes, since a session bs started finds its project from its pane.
- `QUEEN.md` and `WORKER.md` lose their PR wording to `internal/prompt/delivery/<kind>.md`; the bake-off arms are unchanged for `github_pr` (bees-12.3).
- `bs config`'s fixed header line about the only harness goes; the `harness` key now says it.
- A second harness is one package. omp's also ships a bs extension, for the ring only: bs-owned code inside another program, which meets SEC-1.
