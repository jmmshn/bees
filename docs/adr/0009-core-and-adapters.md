# bees is a portable core plus three adapters picked in config

> Supersedes S4 (Claude Code only, behind `{Launch, Ring, Alive}`). Amends [ADR-0004](0004-liveness-is-derived-not-stored.md), [ADR-0006](0006-spawn-and-naming.md), [ADR-0007](0007-claudes-registry-confirms-a-launch.md) and [ADR-0008](0008-config-files-one-tagged-struct.md). Epic bees-12; security criteria from bees-12.2.

bees-12's north star (the same as bees-11's): a new user on another harness (Gemini's agy, oh-my-pi) and another VCS (a CitC client and CLs instead of a git worktree and PRs) can set bees up with agentic help, and the config shows every decision about what runs where. The fact finding (queen-bees, 2026-10-21) found Claude Code wired into about 12 places with no interface, git in identity, spawn and prune, and 56 PR and branch mentions in `QUEEN.md`/`WORKER.md`. S4, ADR-0006 and ADR-0008 each assumed one of those three things.

So bees is a **core** plus **three adapters**, each picked in config:

```toml
harness = "claude_code"   # top level, global file only: every queen and worker on the machine

[workspace]
kind = "git_worktree"     # git_worktree | command | none

[delivery]
kind = "github_pr"        # github_pr | manual | command

[guards]
deny = [...]              # default derived from workspace and delivery; each harness spells it
```

- **The core** is the logbook (ADR-0001, 0002, 0005), the role prompts, the bead → name → session binding (ADR-0006), spawn, end and prune as sequences of adapter calls, messaging, and identity checks. It names no harness, VCS or code host.
- **Fixed, not adapters:** herdr (it already starts 24 agent kinds, and `workspace create` needs no git) and bd (it only feeds the brief and the queen's bead commands).
- Each adapter is a Go interface in `internal/harness`, `internal/workspace` and `internal/delivery`, with one package per kind and a stateful fake beside it that encodes the real tool's constraints (motif 13). Today's behaviour becomes `claude_code`, `git_worktree` and `github_pr`, with no change for a user who sets nothing.

## Harness

The harness is the agent program in the pane: how it starts, how it is rung, and how it spells guards. Where a session is running, and which session bs runs in, are not the harness's business: herdr answers both for every harness (keeper, 2026-10-21, after the probe below). SPEC's `{Launch, Ring, Alive}` was the right idea at too coarse a grain: a launch has three parts (herdr's kind, the per-start files and argv, a check before it), and `Alive` moves into the core. Cleanup is not a method either: `Prepare` returns the files it wrote, the spawn entry records them, and `bs end` removes what was recorded (motif 12).

```go
package harness

type Priority string // "now" | "next"

// Start is everything the core decides about one launch. Adapters render it; they
// never choose a name, a directory or a prompt.
type Start struct {
	Role       prompt.Role       // Queen | Worker: the one Role type, shared with Delivery.Prompt
	Name       string            // session name (ADR-0006)
	Dir        string            // the workspace directory
	PromptFile string            // role prompt + BEADS.md + the delivery fragment
	Doorbell   string            // first user message; "" for the queen
	Continue   bool              // resume the last conversation in Dir
	Env        map[string]string // BS_PROJECT, BS_LOGBOOK_DIR, BS_LOGBOOK="", BS_SESSION, BS_HARNESS
	Guards     guards.Rules      // deny patterns and allow intents, harness-neutral
}

type Launch struct {
	Program string            // for bs queen's exec: "claude", "omp", "agy"
	Args    []string          // after `herdr agent start … --`, or after Program for the queen
	Files   []string          // per-start private files: recorded in the spawned entry, removed by bs end
	Record  map[string]string // other facts the spawned entry must keep (omp: the profile, which --continue depends on)
}

// Target is a Live session as the core found it: from the logbook's record and herdr.
type Target struct {
	Name, Pane string
	PID        int // the agent's pid, from herdr pane process-info
}

// An adapter is built once per command from its own config table
// (claude.New(cfg.Worker.ClaudeCode)), so no launch carries an untyped table.
type Harness interface {
	Kind() string                                    // herdr --kind: claude | omp | agy; also what herdr reports as the pane's agent
	Prepare(st Start) (Launch, error)                // write private files, build argv
	Preflight(dir string) (warn []string, err error) // err refuses the launch (SEC-3); warn: Claude's untrusted repo
	Ring(t Target, text string, p Priority) error    // the doorbell
}
```

**Liveness and identity come from herdr, for every harness.** Every session bs starts runs in a herdr pane it records: a worker's in its `spawned` entry, the queen's in a new `queen` entry that `bs queen` writes from `HERDR_PANE_ID` (herdr sets it in every pane) before it execs the harness. Then:

- **State** is `herdr pane get <recorded pane>`: `agent` equal to `Kind()` is *Live*, or *Blocked* when `agent_status` is `blocked` (a startup prompt: tell the keeper the pane); `agent` null is *Absent* (a message waits; spawn may start or restart); launched and not yet Live is *Starting* (wait up to 20 s, ADR-0007). A herdr error is an error, never Absent (motif 7). Read `agent`, not `agent_status`: right after a crash the status still says `idle` for a moment while `agent` is already null. The agent's pid, when an adapter needs it, comes from `herdr pane process-info`.
- **Identity** (`project.Me`) is `HERDR_PANE_ID` matched against the open `spawned` and `queen` entries. Inside a recorded pane, an `--as` naming anyone else is refused, the same guard as today's ancestor walk; outside herdr, `--as` and `BS_SESSION` apply as now. The launch-checkout check reads the entry's recorded directory.
- **Outcomes** are neutral: a start is started, waiting (Blocked) or unconfirmed; a ring is rung (written, not proven read), not running or failed, and none fails the send; a resume continues or starts fresh, and a harness that can't tell must fail visibly. Busy/idle attention (R38) stays out of the port: herdr reports it for claude and agy, but read omp as idle while it worked in bees-11.11.

The probe (herdr 0.9.5, a throwaway workspace, 2026-10-21; `pane get` and `process-info` on the root pane):

| check | claude | omp | agy |
|---|---|---|---|
| started: `agent` | `claude` | `omp` | `agy` |
| at its trust prompt | `agent_status: blocked` | n/a | `blocked` |
| working, during a long tool call | `working` | not run (API cost) | not run (quota) |
| clean exit (`/exit`) | `agent: null` within 2 s | `null` | not run |
| `kill -9` | `null` within 0.2 s | `null` within 0.5 s | `null` within 0.5 s |

So nothing is built for a harness that has no registry: omp and agy need no bs-owned entry, no hook writing one, and no stale-entry handling. Claude's own registry stays, read only inside `claude_code`'s `Ring` (the `<pid>.json` for the agent's pid gives the socket and the token).

How the old seams map: `Launch` is `Kind` + `Prepare` (herdr still runs the process; `bs queen` execs `Program`); `Alive` is the core reading the recorded pane; `Ring` keeps its job but takes a `Target`. `List`, `Find` and `Self` are gone from the port.

What `claude_code` does for each, the code that moves behind it (bees-12.6):

| Method | claude_code | Today in |
|---|---|---|
| `Kind` | `claude` | `HerdrCLI.StartClaude` |
| `Prepare` | `--settings` file (crossSessionInbound, env, autoMode, permissions.deny), `--name`, `--append-system-prompt-file`, `--model=`, `--continue` | `worker.workerSettings`, `Fleet.launch`, `cmd/bs/queen.go` |
| `Preflight` | `~/.claude.json` trust warning; never refuses | `claude.Trusted` |
| `Ring` | `~/.claude/sessions/<pid>.json` → socket and `.key` token, then NDJSON | `claude.Ring`, `claude.Find`, `bs send` |
| (cleanup) | `Launch.Files` = the `<name>-settings-*.json` file | `bs end`, `Fleet.start` |
| (moves to the core: herdr) | liveness and identity | `claude.List`, `ClaudeSessions`, `bs status`, `project.registered` |

**Ringing a harness with no ring.** omp's ring is a bs extension listening on `$XDG_RUNTIME_DIR/bees/<name>.sock` (a path built from the name, not looked up), with a token file beside it (SEC-1). agy has no proven ring (bees-12.7): until one is, an agy worker reads mail only when it next runs `bs inbox`, and `bs send` says it couldn't ring.

**Env pinning.** `Start.Env` is the contract: every process the session's tools start must see it. Two ways reach it. herdr's `workspace create --env K=V` reaches the root pane's shell and the agent `herdr agent start` launches there (bees-12.4, checked in `/proc/<pid>/environ`), but not a pane split later, so bs only starts agents in a workspace's root pane. `herdr worktree create` has no `--env`, so under `git_worktree` the harness pins it: Claude keeps `--settings` `env`, which also beats a stale inherited value. A harness with no native pin (omp) needs one of: bees-12.5 moving `git_worktree` to `git worktree add` plus `workspace create --env`, or the adapter's own way (bees-12.8 picks). The default path does not change.

**Adapter requirements** (from the bees-11.11 review; bees-12.8 and any later adapter meet them):

- **Resume.** The core sets `Continue` only when the worker has read a message since this spawn (ADR-0007). `Prepare` must then resume that conversation or fail visibly, never start a fresh one silently or pick an older spawn's transcript. omp's `--continue` takes the newest session in the directory, and nothing says it exits when there is none: probe it. A resumed session keeps its recorded system prompt, so role-prompt changes reach it only on a fresh start.
- **Ring priority.** `Now` interrupts the turn and `Next` waits for it (omp: steer and followUp). No mapping may fold a doorbell into context without a turn (omp's aside in plan mode).
- **The prompt file** is checked readable just before launch, by the core for every harness: omp silently falls back to no prompt when it can't read one.
- **Leftovers outside `Launch.Files`** (omp's transcripts and `~/.omp/logs/http-*` dumps) are the harness's own state, which bs doesn't remove. A new spawn never continues them, since `Continue` needs a receipt since that spawn.
- **agy** (bees-12.7, partial): no `--name` and no system-prompt file flag (a custom agent via `--agent` or a SessionStart `systemMessage` are untested candidates); `-c` falls back to the newest conversation in the tree and never reports "none", so a resume uses `--conversation <id>`, which the adapter must record in `Launch.Record` once agy has made it (how is open: bees-12.7's follow-up); the ring is unproven (`agy agentapi send-message` needs the language server address and CSRF token from agy's own env, and exits 0 on error); guards ride a PreToolUse hook, since agy's `settings.json` permissions are global only; never `--dangerously-skip-permissions`. Hooks (the guards) load only in a trusted folder, so bs never pre-trusts one: a Blocked agy worker waits for the keeper's trust answer, then `bs spawn` restarts it (SEC-3).
- **Shared harness state** across concurrent workers (omp's profile databases and session leases) is unprobed; bees-12.8 probes it before claiming more than one worker.

**The harness key** (amends ADR-0008's "no selector until a second harness"):

- `harness` is a top-level key, global file only, enum of the built-in adapters, default `claude_code`. It names the harness new sessions start on, and one machine runs one: every project's queen and workers. A harness per role would need a second ring and identity path in a fleet, and nobody has asked for that.
- Only the active harness's tables are valid. `[worker.oh_my_pi]` with `harness = "claude_code"` is an error naming the file, the table and the `harness` key, not a plain unknown-key error, so the fix it names is "remove the table or switch back". An adapter that isn't built in is an unknown enum value.
- Each `spawned` entry records the harness, and spawn pins it as `BS_HARNESS`. `bs queen` writes a `queen` entry recording it too. Ringing and teardown use the recorded harness, whatever the config says now (motif 12), and the skip-list commands (ADR-0008) know it without reading the files. So a machine caught mid-switch still delivers every message.
- **Switching while sessions run is refused,** per project. `bs queen` and `bs spawn` refuse (exit 2) when the project has an open `spawned` entry, or a `queen` entry whose session is still live, recorded under another harness. Liveness comes from the recorded pane, so a harness that isn't installed costs nothing. The error names the sessions to `bs end`. `bs end`, `send`, `inbox` and `status` never refuse on a switch: they need no harness table, only the recorded harness. bs does not scan other projects' logbooks, so each project is checked on its own next `bs queen` or `bs spawn`. Until then a machine can briefly run two harnesses, which the recorded-harness routing keeps safe.

## Workspace

The workspace is the directory a worker works in and the herdr workspace opened on it. Today that is always `herdr worktree create` on a git branch.

```go
package workspace

type Want struct {
	Name, Project, Repo, Base, Label string            // Base: what to branch or fork from
	Env                              map[string]string // the same Start.Env, for kinds that open with workspace create --env
}

type Made struct {
	ID, Pane, Dir string // herdr workspace id, its root pane, the working directory
	Ref           string // the kind's own handle: git_worktree's branch, a command script's client id, "" for none
}

type Workspace interface {
	Create(ctx context.Context, w Want) (Made, error)                        // dir and herdr workspace together
	Remove(ctx context.Context, m Made, force bool) error                    // already gone → nil; ErrDirty without force
	Dirty(ctx context.Context, m Made) (bool, error)
	List(ctx context.Context) ([]Made, error) // workspaces this kind made, for orphans and ErrNotOurs
	Release(ctx context.Context, m Made) error // after Landed: drop the kind's handle (git_worktree: git branch -D m.Ref); ErrInUse if a checkout holds it
}
```

- **`git_worktree`** (default) is today's path, unchanged: `herdr worktree create`, branch `<name>`, directory `<paths.worktrees>/<name>`, orphans matched by herdr's `checkout_path`. It ignores `Want.Env` (see Env pinning).
- **`command`** runs the user's `create`, `remove` and `dirty` scripts with the name, base and project; `create` prints the absolute directory on stdout, and bs opens it with `herdr workspace create`. This is the CitC case.
- **`none`** opens a workspace on the main checkout, one live worker per project.

`bs prune` asks `Delivery.Landed`, then calls `Workspace.Release` on what landed, so the core runs no `git` itself: `git_worktree` deletes the branch (refusing one a checkout still holds, today's in-use check), `command` runs an optional `release` script, `none` does nothing.

herdr constraints the adapters encode (bees-12.4, herdr 0.9.5):

- `workspace create` with a missing `--cwd` exits 0 and opens in `$HOME`, so `Create` checks the directory exists first.
- `workspace list` shows no directory for a plain workspace, and the root pane's reported cwd is an early sample. So `command` and `none` tag the workspace (`herdr workspace report-metadata <id> --token path=<dir> --source bs`; herdr 0.9.5 refuses `--source` before `--token` with "unknown option"), and `List` reads the tag. Orphans and `ErrNotOurs` are matched by that recorded path, never by label or by a live cwd that moves on `cd`. A workspace with no tag (made by hand, or before the tag existed) has no known path: that is *unknown*, reported and never removed, not a mismatch.
- `workspace close` leaves the directory alone, and a second close fails with `workspace_not_found`, which `Remove` reads as "already gone".

## Delivery

Delivery is what "done" means and how bs proves a worker's work has landed.

```go
package delivery

type Verdict int // Landed | NotLanded | Unknown | NotChecked

type Delivery interface {
	// Landed has no error return: a probe that fails is Unknown, and why says what failed
	// (motif 11: one spelling for "could not check").
	Landed(ctx context.Context, sp logbook.Spawn) (v Verdict, why string)
	Guards(ctx context.Context, repo, ref string) (guards.Rules, error) // deny patterns and allow intents; bs config passes ref "<worker>"
	Prompt(role prompt.Role) string                                     // the role prompts' delivery fragment
}
```

- **`github_pr`** (default): `Landed` is today's three proofs (ancestry, a merged PR on the branch tip, identical paths: R42); `Guards` is today's push and merge matrix, against the repo's real default branch, which it looks up itself (bees-11.7); `Prompt` is today's PR wording, moved word for word out of `QUEEN.md` and `WORKER.md` (bees-12.3).
- **`manual`**: the worker reports `done: <bead>`, the keeper closes the bead, `Landed` is `NotChecked`, so `bs prune` releases nothing and only reports what it skipped (`bs end` already removed the workspace).
- **`command`**: a user script answers `Landed`. Named now so the enum is stable; bees-12.3 builds it only if it is trivial.

`[guards] deny` is harness-neutral: shell-glob patterns over a command line. Its default is the delivery's `Guards`. Each harness renders the rules in its own syntax: Claude's `permissions.deny` and autoMode allow prose, omp's `bash.patterns`. Guards stay a speed bump (SPEC); branch protection, or the VCS's own review gate, is the real one.

## Naming (amends ADR-0006)

The name is still one forward function of the bead (`bees-12.3` → `bees-12-3`) and is the session name, the herdr agent name and the message address. It is the git branch and the `-WT` worktree directory only under `git_worktree`; `command` receives it and may use it (a client named after it), and `none` has neither. Whatever the kind makes is recorded in the `spawned` entry (`Made`), and teardown uses what was recorded, never a name rebuilt from config (motifs 3, 12). Project identity without git comes from `--project`/`BS_PROJECT` and `paths.repo` (bees-12.5).

## Security acceptance criteria (bees-12.2)

Every harness adapter, the claude_code extraction (bees-12.6) and the omp adapter (bees-12.8) included, must meet these before it ships. Same-uid processes can't be fenced from each other (bees-8.8), so the bar is: nothing another user or another machine can reach, and nothing weaker than Claude Code's own path.

- **SEC-1, ring authentication.** The ring endpoint is a unix socket mode 0600 in a directory mode 0700 (no TCP, no abstract socket). A bs-owned endpoint also requires a random per-session token of at least 128 bits, kept in a 0600 file beside the socket, and drops a connection without it; bs never rings a bs-owned endpoint with no token. Claude Code (2.1.306, T1 below) meets the file-mode half and no more: sockets 0600 under `/run/user/<uid>/cc-socks` (0700), registry `~/.claude/sessions` 0700, `.key` files 0600, but it acted on a ring with no auth line and on one with a wrong token, though the session had a key file. Its boundary is the uid, and bs sending the token adds nothing. That is no weaker than bees-8.8's same-uid limit, so `claude_code` ships as it is (bees-12.6 changes no behaviour), and the gap is Claude's to close.
- **SEC-2, no stale liveness.** A session is Live only while herdr reports its harness as the agent of the pane the logbook recorded; a pane whose spawn has ended names no one, so a reused pane id can't be mistaken for a worker. A herdr error is an error, never Absent (motif 7). Where an adapter still reads a harness's own registry (claude_code's `Ring`), it reads only the entry for the pid herdr reports and checks its procStart before trusting the socket path. Today's `claude.List` fails open (`filepath.Glob` drops I/O errors, an unreadable file is skipped as if its session had exited); it leaves the liveness path, and the `Ring` read must not repeat that.
- **SEC-3, a trust step.** A launch either goes through the harness's own trust step (Claude's folder-trust prompt, which `Preflight` warns about), or disables the repo's ambient extensions, hooks and config (omp: `--trusted-extension <bs extension>`, plus a per-start `--config` overlay above the project layer). Deny rules sit in a layer the repo's own files can't override. A harness that can do neither is refused at spawn.
- **SEC-4, no secrets in bees** (added here; not one of bees-12.2's three findings, but the bees-11.11 key rules are the same kind of requirement). API keys and OAuth tokens stay in the harness's own auth (omp login or its models file; agy's Google login). bs never writes them to config, the logbook, a pinned env var, a per-start file or herdr.

Test plan, run by the bead that builds each adapter:

| Id | Check | How |
|---|---|---|
| T1 | Claude drops a ring without the token, or doesn't | **done** (bees-12.1, 2026-10-21): NDJSON to this session's own socket, once with no auth line and once with a wrong token: both arrived as user turns. Re-run on a Claude upgrade |
| T2 | the bs-owned socket refuses no token and a wrong token | unit test against the extension's listener; live-test with `socat` |
| T3 | modes 0700/0600 on the socket dir, socket and token file | unit test after `Prepare`, and a check in `bs doctor` (bees-12.9) |
| T4 | a reused pane or pid names no one | fake herdr: an ended spawn's pane now running the harness is not Live; claude_code's `Ring` refuses a `<pid>.json` whose procStart doesn't match |
| T5 | herdr down fails closed | herdr socket unreachable: spawn refuses, `bs status` errors, `bs send` stores and says it couldn't ring |
| T6 | a repo's own hook can't run in or rewrite a worker | toy repo with an `.omp/hooks` (or the harness's equivalent) `tool_call` hook that writes a marker file and rewrites `git push` → spawn, push from the worker: no marker, and the deny rule still fires |
| T7 | a repo config can't loosen the guards | toy repo whose project config allows `git push origin main`: the push is still denied |
| T8 | no secret reaches bees | after a live run, grep the logbook, config, cache dir, per-start files and `/proc/<pid>/environ` of the pane shell for the key's prefix: none |

## Considered Options

- **A harness per role** (`queen.harness`, `worker.harness`; bees-11.11's first sketch). Rejected by the keeper: one harness per machine is enough for the north star, and it keeps one ring and identity path in each running fleet.
- **A bs-owned registry, written by the harness's extension (omp) or a SessionStart hook running `bs hello` (agy), bound to the spawn entry.** Round 2's pick. Rejected once herdr proved to report each harness's agent in its recorded pane: it was a second truth to keep fresh (motif 1), with stale entries, a binding rule and, for agy, a hook that loads only in a trusted folder.
- **A `bs hello` the model runs because its prompt says so.** Rejected: a prompt step is not a process fact (motif 9).
- **Claude's registry for Claude, herdr for the rest.** Rejected by the keeper: two liveness and identity paths in the core.
- **Workspace and delivery as one VCS adapter.** Rejected: they vary apart. A git worktree can deliver by PR or by hand, and a CitC client delivers by CL, which a `command` delivery can check.
- **Moving `git_worktree` to `git worktree add` plus `workspace create --env` now,** so env has one path for every harness. Deferred to bees-12.5: it changes today's default path and drops herdr's `checkout_path`, which orphan and `ErrNotOurs` matching read.
- **Making git a fixed dependency, like herdr and bd.** Rejected: the CitC case is the north star.

## Consequences

- `internal/harness/claude` becomes the `claude_code` adapter behind `Harness`; the core packages stop importing it (bees-12.6). Existing tests must pass unchanged.
- Liveness and identity move from Claude's registry to the recorded herdr pane for every harness, Claude included: a behaviour change, so it lands as its own stage after the extraction, with `bs status`, the twin check, start confirmation, `project.Me` and the `--as` guard re-live-tested. It amends ADR-0004 (liveness from herdr, not Claude's registry) and ADR-0007 (herdr's report on the recorded pane confirms a start; herdr's `timeout` stays unconfirmed). `bs queen` records its pane from `HERDR_PANE_ID`, so a queen started outside herdr can't be rung or seen by `bs status`.
- The `spawned` entry's data gains `harness`, `workspace` (kind) and the kind's `Made` fields and the harness's `Launch.Files` and `Record`; `Branch` becomes `git_worktree`'s `Ref`, and old entries read as `claude_code` and `git_worktree`. A `queen` entry kind records the queen's harness.
- `QUEEN.md` and `WORKER.md` lose their PR wording to `internal/prompt/delivery/<kind>.md`; the role prompts the bake-off replays are unchanged for `github_pr` (bees-12.3).
- `bs config`'s fixed header line about the only harness goes; the `harness` key now says it.
- A second harness is an adapter package. omp also gets a bs extension, for the ring only: bs-owned code inside another program, which has to meet SEC-1 and SEC-3.
