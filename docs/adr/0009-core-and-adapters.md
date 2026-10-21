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
// made, and cleanup is the forward glob <name>-* (motif 3). bs queen execs the binary
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
// function. Kind and Ring need no config table, so bs send and bs status build it without
// reading the config files (ADR-0008's skip list); Prepare also needs the role's table
// (claude.New(cfg.Worker.ClaudeCode)).
type Harness interface {
	Kind() string                                 // herdr --kind: claude | omp | agy; also what herdr reports as the pane's agent
	Prepare(st Start) (Launch, error)             // pure: render guards, files and argv
	Ring(t Target, text string, p Priority) error // the doorbell
}
```

Cleanup is not a method: the core removes `<name>-*` from the cache dir on `bs end` and before each start. There is no `Start.Env`: bs pins no env, and a session finds its project and name from its herdr pane (amendment 1).

**Liveness and identity come from herdr, for every harness** (keeper, after the probe below). Every session bs starts runs in a herdr pane it records: a worker's in its `spawned` entry, the queen's in a new `queen` entry that `bs queen` writes from `HERDR_PANE_ID` (herdr sets it in every pane) before it execs the harness. `bs queen` outside a herdr pane refuses with exit 2 (a queen bs can't see is a queen no worker can ring), and so does a second `bs queen` while the newest `queen` entry's pid is alive.

- **Naming.** Once a start is confirmed, bs names the agent in herdr (`herdr agent rename <pane> <name>`; ADR-0007 deferred it until something needed it). herdr keeps a name with its process, unique among live agents, and clears it when the agent exits. A pane the keeper moves (`herdr pane move`) gets a new id, and a workspace whose last pane moved out closes (probed); the name follows the agent there. A second `herdr agent start` with a live agent's name fails with `agent_name_taken` and starts nothing (probed), so a named worker is also fenced against a twin at launch (motif 7); only the window before the rename relies on bs's own twin check.
- **State** of a worker is `herdr agent get <name>`, else `herdr pane get <recorded pane>` (a start not yet named). The queen is never named (`bs queen` execs, so nothing renames her), so her state is her recorded pid, alive with its recorded start time (`proc.Start`), which survives a pane move. `agent` equal to `Kind()` is *Live*, or *Blocked* when `agent_status` is `blocked` (a trust dialog at start, or a permission prompt later: at start bs tells the keeper which pane). A Blocked session counts as running for the twin check and is rung like a Live one: the message is stored first, and the harness shows it when the prompt clears. `agent` null, or `pane_not_found` for a closed pane (probed), is *Absent* (a message waits; spawn may start or restart). Another harness's agent in the recorded pane is *Foreign*: spawn refuses, and nothing rings it. Launched and not yet Live is *Starting* (wait up to 20 s, ADR-0007). A herdr error is an error, never Absent (motif 7). Read `agent`, not `agent_status`: right after a crash the status still says `idle` for a moment while `agent` is already null.
- **Teardown of a moved worker.** `bs end` finds the recorded workspace gone but the named agent Live in another pane: it refuses ("bees-12-3 now runs in pane wSY:p2; close it there, or bs end --force") rather than recording `ended` over a running worker (motif 12).
- **Identity** (`project.Me`) is `HERDR_PANE_ID` matched against the open `spawned` and `queen` entries, counted only while that pane's agent is the one the entry started: for a worker, the herdr name bs gave it; for the queen, the pid and start time `bs queen` recorded, which `exec` keeps. After the queen exits, neither the keeper's shell left in her pane nor a later Claude started there is the queen. A moved worker keeps its old `HERDR_PANE_ID`, and herdr keeps resolving that id for the moved process's own calls (herdr's docs; probed). When herdr is down, identity falls back to the recorded pane alone, with a warning, so `bs inbox` and `bs send` still work; liveness checks fail closed (spawn refuses). A herdr call costs about 2 ms (`pane get` and `process-info`, median of 20), so every command can afford it. Inside a recorded pane, an `--as` naming anyone else is refused, the same guard as today's ancestor walk. Outside a recorded pane, `--as` and `BS_SESSION` apply as now (amendment 1).
- **Outcomes:** a start is started, waiting (Blocked) or unconfirmed; a ring is rung (written, not proven read), not running, unsupported (`ErrNoRing`: agy until its ring is proven) or failed, and none fails the send; a resume continues or starts fresh, and a harness that can't tell must fail visibly. Busy/idle (R38) stays out: herdr reports it for claude and agy, but read omp as idle while it worked in bees-11.11.

The probe (herdr 0.9.5, a throwaway workspace, 2026-10-21; `pane get` and `process-info` on the root pane):

| check | claude | omp | agy |
|---|---|---|---|
| started: `agent` | `claude` | `omp` | `agy` |
| at its trust prompt | `agent_status: blocked` | n/a | `blocked` |
| working, during a long tool call | `working` | not run (API cost) | not run (quota) |
| clean exit (`/exit`) | `agent: null` within 2 s | `null` | not run |
| `kill -9` | `null` within 0.2 s | `null` within 0.5 s | `null` within 0.5 s |

So nothing is built for a harness without a registry. Claude's own registry stays, read only inside `claude_code`'s `Ring`: the `<pid>.json` for the pid herdr reports gives the socket and the token.

What `claude_code` does, and the code that moves behind it (bees-12.6):

| Method | claude_code | Today in |
|---|---|---|
| `Kind` | `claude` | `HerdrCLI.StartClaude` |
| `Prepare` | `--settings` file (crossSessionInbound, env, autoMode, permissions.deny), `--name`, `--append-system-prompt-file`, `--model=`, `--continue` | `worker.workerSettings`, `Fleet.launch`, `cmd/bs/queen.go` |
| `Ring` | `~/.claude/sessions/<pid>.json` → socket and `.key` token, then NDJSON | `claude.Ring`, `claude.Find`, `bs send` |
| (cleanup, core) | `<name>-*` in the cache dir | `bs end`, `Fleet.start` |
| (to the core: herdr) | liveness and identity | `claude.List`, `ClaudeSessions`, `bs status`, `project.registered` |
| (dropped) | the untrusted-repo warning: a trust prompt now shows as Blocked | `claude.Trusted`, `bs spawn` |

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
- **herdr reports `agent` before the start returns.** In the probe, a Claude busy on its first prompt was `agent: claude` within 3 s, while `herdr agent start` timed out at 10 s and `herdr agent get <name>` then said `agent_not_found` (ADR-0007's case); `agent_status` also took the value `done`. The core keys on `agent` only.

**The harness key** (amends ADR-0008's "no selector until a second harness"):

- `harness` is a top-level key, global file only, enum of the built-in adapters, default `claude_code`. It names the harness new sessions start on, and one machine runs one (keeper, 2026-10-21).
- Only the active harness's tables are valid. `[worker.oh_my_pi]` with `harness = "claude_code"` is an error naming the file, the table and the `harness` key.
- Each `spawned` and `queen` entry records the harness. Ringing and teardown use the recorded harness, whatever the config says now (motif 12), and the skip-list commands (ADR-0008) know it without reading the files.
- `bs queen` and `bs spawn` refuse (exit 2) when the project has a live session recorded under another harness, naming the sessions to `bs end`. `bs end`, `send`, `inbox` and `status` never refuse on a switch. Each project checks on its own next start; bs scans no other project's logbook.

## Workspace and delivery are config, not ports

**Workspace** is a switch inside the core, with no hooks (amendment 1):

- **`git_worktree`** (default) is today's path: branch `<name>`, directory `<paths.worktrees>/<name>`, orphans matched by herdr's `checkout_path`, `bs end` refusing a dirty worktree without `--force`, `bs prune` deleting merged branches (R42).
- **`none`** opens a herdr workspace on `paths.repo` (`herdr workspace create --cwd`, after checking the directory exists: a missing `--cwd` exits 0 and opens `$HOME`). bs makes and removes no directory: the keeper makes it (a CitC client) and points `paths.repo` at it, and `bs end` only closes the workspace. The queen decides how many workers share an unhooked checkout. bs tags the workspace with its path (`herdr workspace report-metadata <id> --token path=<dir> --source bs`; herdr 0.9.5 refuses `--source` before `--token`), since `workspace list` shows no directory for a plain workspace; an untagged one is *unknown*, reported and never removed.

The `spawned` entry records the directory, and every later step uses the record, never a dir rebuilt from config (motif 12). `bs end` refuses before it closes anything: on a dirty worktree (`git_worktree` only) and on background work (amendment 1), both without `--force`.

**Delivery** sets three things, none a port: the role prompts' delivery fragment, the guards' default, and which proofs `bs prune` uses. Under `none` there are no branches, so `bs prune` has nothing to do. The fragment is `internal/prompt/delivery/<kind>.md`, assembled like `BEADS.md`:

- **`github_pr`** (default) is today's PR wording, moved word for word out of `QUEEN.md` and `WORKER.md`, so prompts don't change for today's users (bees-12.3).
- **`manual`**: the worker reports with `bs done --report`, the keeper closes the bead. `bs prune` uses only its git proofs (ancestry, identical paths), never GitHub.
- **`fragment`** names a file of the user's own wording, for a CL flow or anything else; the queen judges when such work has landed.

**Guards** stay in config (keeper, round 3). `[guards] deny` holds harness-neutral shell-glob patterns over a command line. Empty means the delivery kind's default: for `github_pr`, today's push and merge matrix against the repo's real default branch (bees-11.7); for `manual`, no push to the default branch. Each harness renders them in `Prepare`: Claude's `permissions.deny` and autoMode allow prose, omp's `bash.patterns` in a per-start `--config` overlay, agy's PreToolUse hook. They are a speed bump; branch protection, or the VCS's own review gate, is the real guard.

## Naming (amends ADR-0006)

The name is still one forward function of the bead (`bees-12.3` → `bees-12-3`) and is the session name, the herdr agent name and the message address. It is the git branch and the `-WT` worktree directory only under `git_worktree`. Whatever a spawn made is recorded in its `spawned` entry, and teardown uses that record, never a name rebuilt from config (motifs 3, 12). A session's project comes from its pane's entry (amendment 1); a process the keeper starts by hand uses `--project`/`BS_PROJECT`, else git, else `paths.repo`.

## Amendment 1: pane identity, bs done and the background-work gate

Keeper decisions, 2026-10-21 (bees-12.16), after the stage beads were filed. They change the text above in place; this section holds the detail.

**1. Identity comes from the pane; bs pins no env.** A session's identity is `HERDR_PANE_ID` (herdr sets it in every pane) → the open `spawned` or `queen` entry that recorded that pane → its project and name. The logbook directory is the global file's `paths.logbook_dir`, so every process reads the same one.

```text
pane = $HERDR_PANE_ID                              # unset: not a session bs started
dir  = global config paths.logbook_dir             # never the project file, never env
for db in dir/*.db (read only, no create, no migrate):
    open spawned/queen entries whose data.pane = pane
one match → its project and name, then the liveness check (worker: herdr name; queen: pid + start)
none      → not a recorded pane: --project, BS_PROJECT, git, paths.repo as before
two+      → keep those that pass the liveness check; still two+ → error naming both (motif 4)
```

- **The scan, not an index.** A machine holds tens of logbooks, each opened read only in about a millisecond. An index would be a second truth (motif 1); if the scan ever gets slow, one can be added as a rebuildable projection, as ADR-0002 allows for unread.
- **`paths.logbook_dir` becomes global only** (amends ADR-0008): a project file's value would put that project's logbook where no pane scan looks.
- **Inside a recorded pane the pane decides.** An inherited `BS_LOGBOOK`, `BS_LOGBOOK_DIR` or `BS_PROJECT` (herdr's server env, a keeper's shell) is ignored with a warning when it disagrees, which is what the pins used to guard. `send`, `inbox` and `done` refuse a `--project` naming another project, as they refuse `--as`; read-only commands (`bead ls`, `status`) may read another one.
- **`BS_LOGBOOK`, `BS_LOGBOOK_DIR` and `BS_PROJECT` stay for processes the keeper starts by hand** outside a recorded pane: tests, scripts, a one-off `bs send`, a copied logbook. `bs queen` and `bs spawn` refuse (exit 2) when the logbook they resolve is not `<global logbook_dir>/<project>.db`, since the fleet's panes could never find it.
- **The skip list** (ADR-0008: `send`, `inbox`, `status`, `bead`, `epic`, `version`) is keyed on `BS_LOGBOOK_DIR`, which sessions no longer carry. Inside a herdr pane those commands read only the global file's `paths.logbook_dir`: an error in that key or a TOML syntax error fails them, an error anywhere else doesn't.
- **When herdr is down,** the recorded pane alone names the session, with a warning: the scan needs only the env var and the logbooks.

What bees-11.3 shipped that goes: the `env` block in a worker's `--settings` (`BS_PROJECT`, `BS_LOGBOOK_DIR`, `BS_LOGBOOK=""`, in `worker.workerSettings`), `Project.LogbookDir` and its spawn refusal for a `BS_LOGBOOK` not named `<project>.db` (replaced by the refusal above), and the skip list's `BS_LOGBOOK_DIR` trigger (replaced above). `bs config`'s note when `BS_LOGBOOK_DIR` is set stays. R46's `BS_PROJECT` pin goes too: a `--repo` worker's project is the one whose logbook recorded its pane. Never built, now dropped: `Start.Env`, the `BS_SESSION` and `BS_HARNESS` pins (bees-12.10), the `git worktree add` plus `workspace create --env` path for a harness with no native pin (bees-12.5), and omp's and agy's env file (bees-12.8, 12.12). Every `git_worktree` worker opens with `herdr worktree create` and nests; `none` opens with `workspace create --cwd` and no `--env`.

**2. `bs done` is a logbook entry.** `bs done --pr <url>` or `bs done --report <text>` (one is required; the delivery fragment says which) writes a `done` entry: actor the worker, subject its queen, ref its open `spawned` entry, data `{bead, pr}` or `{bead, report}`. It replaces the free-text `done: <bead> PR #N` message. Unread widens from messages to messages and `done` entries addressed to me with no receipt from me (amends ADR-0002), so the queen's `bs inbox` shows it and `bs done` rings her like a send. `bs status` shows the newest `done` since the open spawn. A second `bs done` (an amended PR) is a new entry, and the newest wins. `bs done` refuses outside a recorded pane and while the gate below finds background work. Closing the bead stays with the queen or keeper (motif 5).

**3. The background-work gate, for every harness.** Once a start is confirmed Live, bs records a `started` entry (ref the `spawned` entry): the agent's pid and start time, from `herdr pane process-info --pane <recorded pane>` (the foreground process whose name is `Kind()`; herdr 0.9.5 also gives `shell_pid` and `foreground_process_group_id`), and the baseline: every descendant of that pid as (pid, start time). At `bs done` and `bs end`, a live descendant that isn't in the newest `started` baseline, and isn't the calling `bs` or one of its ancestors, is background work. Both refuse, listing each one's pid, command and any TCP port it listens on:

```text
bs end: bees-12-3 still runs background work; bs end --force ends it anyway
  pid 41822  python3 -m http.server 8000   listening :8000
  pid 41910  go test ./...
```

- Descendants are found by parent pid, not by the pane's tty: Claude runs each Bash call in a new session with no controlling tty (probed, Claude Code 2.1.306). Linux reads `/proc/*/stat`; macOS uses `/bin/ps` (R47). Ports come from `/proc/<pid>/fd` socket inodes matched to LISTEN rows in `/proc/net/tcp{,6}`, else `lsof` by absolute path (from bees-6.10's plan).
- A process that detaches (`setsid` plus a double fork, `nohup … &` from a shell that exits) is reparented away and escapes the walk. The gate catches the common cases, a backgrounded tool call or a server the agent started; it is a speed bump, like the guards.
- A helper the harness starts after Live (a lazily started MCP server) shows as new; the stage bead probes each harness for these before shipping.
- Bounded (motif 15): the herdr call and the walk share one deadline (5 s). Any error, a missing `started` entry included, fails closed (motif 7) with its own exit code and message, "bs can't check for background work: …", never the refusal above and never "none".
- `bs end --force` overrides both refusals; the queen asks the keeper first (`QUEEN.md`). `bs done` has no `--force`: the worker stops its background work, or reports a check error to the queen, who can end it.

This supersedes bees-6.10's port-only check.

**4. No `init` or `finalize` hooks.** `none` opens on `paths.repo`, a directory the keeper made (a CitC client). Hooks come later, if ever.

**5. Delivery fragments shape only the wording:** PR or CL, `bs done --pr` or `--report`. The rest of delivery is bees-12.3's.

## Security acceptance criteria (bees-12.2)

Every harness adapter, the claude_code extraction (bees-12.6) and the omp adapter (bees-12.8) included, meets these before it ships. Same-uid processes can't be fenced from each other (bees-8.8), so the bar is: nothing another user or another machine can reach, and nothing weaker than Claude Code's own path.

- **SEC-1, ring authentication.** The ring endpoint is a unix socket mode 0600 in a directory mode 0700 (no TCP, no abstract socket). A bs-owned endpoint (omp's extension) also requires a random per-session token of at least 128 bits, kept in a 0600 file beside the socket, and drops a connection without it. Claude Code (2.1.306, T1) meets the file-mode half and no more: it acted on a ring with no auth line and on one with a wrong token, though the session had a key file. Its boundary is the uid, no weaker than bees-8.8's limit, so `claude_code` ships as it is, and the gap is Claude's to close.
- **SEC-2, no stale liveness.** A session is Live only while herdr reports its harness in the pane the logbook recorded; a pane whose spawn has ended names no one. A herdr error is an error, never Absent (motif 7). claude_code's `Ring` reads only the `<pid>.json` for the pid herdr reports and checks its procStart. Today's `claude.List` fails open (`filepath.Glob` drops I/O errors, an unreadable file is skipped); it leaves the liveness path, and the `Ring` read must not repeat that.
- **SEC-3, repo hooks** (bees-12.2's third finding: omp auto-loads a repo's extensions and hooks). Dropped by the keeper, round 3: bees runs on the keeper's own repos, and a harness's own trust prompt shows up as Blocked. omp still launches with `--trusted-extension <bs extension>`, which bees-12.8 needs anyway to load the ring extension alone.
- **SEC-4, no secrets in bees** (added; not one of bees-12.2's findings). API keys and OAuth tokens stay in the harness's own auth. bs never writes them to config, the logbook, an env var, a per-start file or herdr.

Test plan, run by the bead that builds each adapter:

| Id | Check | How |
|---|---|---|
| T1 | Claude drops a ring without the token, or doesn't | **done** (bees-12.1, 2026-10-21): NDJSON to this session's own socket, once with no auth line and once with a wrong token: both arrived as user turns. Re-run on a Claude upgrade |
| T2 | the bs-owned socket refuses no token and a wrong token | unit test against omp's extension listener; live-test with `socat` |
| T3 | modes 0700/0600 on the socket dir, socket and token file | unit test after `Prepare`, and a check in `bs doctor` (bees-12.9) |
| T4 | a reused pane or pid names no one | fake herdr: an ended spawn's pane now running the harness is not Live; claude_code's `Ring` refuses a `<pid>.json` whose procStart doesn't match |
| T5 | herdr down fails closed | herdr socket unreachable: spawn refuses, `bs status` errors, `bs send` stores the message and says it couldn't ring |
| T7 | a repo config can't loosen the guards | toy repo whose project config allows `git push origin main`: the push is still denied |
| T8 | no secret reaches bees | after a live run, grep the logbook, config, cache dir, per-start files and `/proc/<pid>/environ` of the pane shell for the key's prefix: none |

T6 (a repo hook rewriting a worker's command) went with SEC-3.

## Considered Options

- **A harness per role** (`queen.harness`, `worker.harness`). Rejected by the keeper: one harness per machine is enough, and it keeps one ring path in a fleet.
- **A bs-owned registry, written by omp's extension or an agy SessionStart hook running `bs hello`, bound to the spawn entry.** Round 2's pick. Rejected once herdr proved to report each harness's agent in its recorded pane: a second truth to keep fresh (motif 1), with stale entries, a binding rule and a hook that loads only in a trusted folder.
- **A `bs hello` the model runs because its prompt says so.** Rejected: a prompt step is not a process fact (motif 9).
- **Claude's registry for Claude, herdr for the rest.** Rejected by the keeper: two liveness and identity paths.
- **Workspace and Delivery ports** (`Create/Remove/Dirty/List/Release` with a `command` kind running user scripts; `Landed/Guards/Prompt` with a `command` kind answering "landed?"). Rounds 1 and 2. Cut in round 3: judging a CL landed is prompt text for a capable worker, and what bs proves stays git-only (`git_worktree`, prune). Making and removing a workspace came back as two formal `init` and `finalize` hooks, then went in amendment 1: under `none` the keeper makes the directory, and hooks come later, if ever.
- **`Preflight` refusing a launch without a trust step (SEC-3).** Cut in round 3 with SEC-3.
- **Moving every `git_worktree` worker to `git worktree add` plus `workspace create --env`,** for one env path. Rejected: it changes the default path and drops herdr's `checkout_path`. Amendment 1 drops the env path for every harness instead.
- **Pinning `BS_PROJECT`, `BS_LOGBOOK_DIR`, `BS_SESSION` and `BS_HARNESS` in each session's env** (the original `Start.Env`). Rejected in amendment 1: a harness with no native pin then needs `workspace create --env`, which doesn't nest, or a bs-written env file its extension or hook loads; the pane already names the session.
- **A port-only teardown check** (bees-6.10: refuse while a descendant holds a listening port). Superseded by the background-work gate, which also catches a build or a test run that holds no port.

## Consequences

- `internal/harness/claude` becomes the `claude_code` adapter behind `Harness`; the core packages stop importing it (bees-12.6). Existing tests pass unchanged.
- Liveness and identity move from Claude's registry to the recorded herdr pane for every harness, Claude included: a behaviour change, so it lands as its own stage after the extraction (bees-12.10), with `bs status`, the twin check, start confirmation, `project.Me` and the `--as` guard re-live-tested. It amends ADR-0004 (liveness from herdr) and ADR-0007 (herdr's report on the recorded pane confirms a start; herdr's `timeout` stays unconfirmed). `bs queen` needs a herdr pane.
- The `spawned` entry's data gains `harness` (the config name, `claude_code`; herdr's kind is derived from it, one forward mapping, motif 3) and the harness's `Record`. Old entries read as `claude_code` and `git_worktree`.
- New entry kinds `queen` (harness, pane, pid, start time; never closed: the newest one is the queen), `started` and `done` (amendment 1) need a logbook migration, one rebuild for all three. The `kind` CHECK constraint can't be altered in SQLite, so the table is rebuilt once inside `BEGIN IMMEDIATE` (the write lock every writer already takes), guarded by `PRAGMA user_version`, after copying the file aside (motif 14). An older shared `bs` keeps working on the rebuilt table: its reads filter by kind and skip `queen` rows, and its writes satisfy the wider CHECK. It ships as its own PR.
- `project.Me` needs the logbook, so `send` and `inbox` resolve identity after opening it; the launch-checkout check that read Claude's registry goes, since a session bs started finds its project from its pane.
- `QUEEN.md` and `WORKER.md` lose their PR wording to `internal/prompt/delivery/<kind>.md`; the bake-off arms are unchanged for `github_pr` (bees-12.3).
- `bs config`'s fixed header line about the only harness goes; the `harness` key now says it.
- A second harness is one package. omp's also ships a bs extension, for the ring only: bs-owned code inside another program, which meets SEC-1.
