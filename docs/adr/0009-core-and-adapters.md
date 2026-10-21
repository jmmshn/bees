# A portable core and three adapters: harness, workspace, delivery

> Supersedes S4 (Claude Code only, behind `{Launch, Ring, Alive}`). Amends [ADR-0006](0006-spawn-and-naming.md) and [ADR-0008](0008-config-files-one-tagged-struct.md). Epic bees-12; security criteria from bees-12.2.

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

The harness is the agent program in the pane: how it starts, where its live sessions are listed, how it is rung, and how it spells guards. SPEC's `{Launch, Ring, Alive}` was the right idea at too coarse a grain. The bees-11.11 omp probe and the code survey show seven seams, because a launch has three parts (herdr's kind, the per-start files and argv, the cleanup of those files) and liveness has two consumers (the spawner checking another session, and bs finding which session it runs in).

```go
package harness

type Priority string // "now" | "next"

// Start is everything the core decides about one launch. Adapters render it; they
// never choose a name, a directory or a prompt.
type Start struct {
	Role       Role              // Queen | Worker
	Name       string            // session name (ADR-0006)
	Dir        string            // the workspace directory
	PromptFile string            // role prompt + BEADS.md + the delivery fragment
	Doorbell   string            // first user message; "" for the queen
	Continue   bool              // resume the last conversation in Dir
	Env        map[string]string // BS_PROJECT, BS_LOGBOOK_DIR, BS_LOGBOOK="", BS_SESSION, BS_HARNESS
	Guards     guards.Rules      // deny patterns and allow intents, harness-neutral
	Table      any               // this harness's own config table ([worker.claude_code])
}

type Launch struct {
	Program string   // for bs queen's exec: "claude", "omp", "agy"
	Args    []string // after `herdr agent start … --`, or after Program for the queen
	Files   []string // per-start private files written for this launch
}

type Session struct {
	Name, Addr, Token string // Addr: socket path; Token: ring secret, "" only where SEC-1 allows
	PID               int
	ProcStart         string // proc.Start format
}

type Harness interface {
	Agent() string                                       // herdr --kind: claude | omp | agy
	Prepare(st Start) (Launch, error)                    // write private files, build argv
	Preflight(dir string) []string                       // warnings before a launch (Claude: repo not yet trusted)
	List(ctx context.Context) ([]Session, error)         // live sessions only (SEC-2); unreadable → error, never empty
	Self() (s Session, ok bool, err error)               // nearest ancestor process that is a live session
	Ring(s Session, text string, p Priority) error       // the doorbell
	Clean(name string) error                             // remove every per-start file Prepare made for name
}
```

How the old seams map: `Launch` is `Agent` + `Prepare` (herdr still runs the process; `bs queen` execs `Program`); `Alive` is the core polling `List` for a name (ADR-0007's 20 s confirmation, and the twin check, which fails closed on an error); `Ring` is unchanged. `Find` is not a method: it is the core picking the newest `List` entry with a name, one code path for every harness. `Self` is what `project.Me` walks today; the core keeps the `--as` guard on top of it.

What `claude_code` does for each, the code that moves behind it (bees-12.6):

| Method | claude_code | Today in |
|---|---|---|
| `Agent` | `claude` | `HerdrCLI.StartClaude` |
| `Prepare` | `--settings` file (crossSessionInbound, env, autoMode, permissions.deny), `--name`, `--append-system-prompt-file`, `--model=`, `--continue` | `worker.workerSettings`, `Fleet.launch`, `cmd/bs/queen.go` |
| `Preflight` | `~/.claude.json` trust warning | `claude.Trusted` |
| `List` | `~/.claude/sessions/<pid>.json` with procStart and the `.key` token | `claude.List`, `ClaudeSessions`, `bs status` |
| `Self` | ancestor walk to a registry file | `project.registered` |
| `Ring` | NDJSON to the cross-session socket | `claude.Ring`, `bs send` |
| `Clean` | `<name>-settings*.json` | `bs end`, `Fleet.start` |

**A registry bs owns, where the harness has none.** Claude Code keeps a registry and a ring socket; omp has neither (bees-11.11) and agy's are being probed (bees-12.7). An adapter whose harness lacks them ships a bs-generated extension or hook that writes a registry entry in bs's own layout (pid, procStart, name, socket, token file; the same fields as Claude's) under `$XDG_STATE_HOME/bees/sessions/<harness>/`, and listens on that socket. `List`, `Self` and `Ring` then share one reader for every bs-owned registry, and only `Agent`, `Prepare`, `Preflight` and `Clean` are written per harness. The registry must be written by the harness process itself (an extension running in it, or a start hook), never by a `bs hello` the agent is asked to run: a model can skip, delay or repeat a step in its prompt, and a registry entry has to exist when the process does (motif 9).

**Env pinning is one path for every harness.** herdr's `workspace create --env K=V` reaches the root pane's shell and the agent `herdr agent start` launches there (bees-12.4, checked in `/proc/<pid>/environ`), so the core pins `Start.Env` there for every harness. It does not reach a pane split later, so bs only ever starts agents in the root pane. An adapter may also pin env its own way (Claude keeps `--settings` `env`, which also beats a stale inherited value) but must not need to.

**The harness key** (amends ADR-0008's "no selector until a second harness"):

- `harness` is a top-level key, global file only, enum of the built-in adapters, default `claude_code`. One harness per machine: the queen and every worker of every project use it. Workers of different harnesses could not share the ring and identity paths without a second selector per role, and nobody has asked for that.
- Only the active harness's tables are valid. `[worker.oh_my_pi]` with `harness = "claude_code"` is an error naming the file, the table and the `harness` key, not a plain unknown-key error. An adapter that isn't built in is an unknown enum value.
- Each `spawned` entry records the harness, and spawn pins it as `BS_HARNESS`, so ringing and teardown use the harness a worker was started with, whatever the config says now (motif 12), and the skip-list commands (ADR-0008) know it without reading the files.
- **Switching while workers run is refused.** `bs queen` and `bs spawn` refuse (exit 2) when the project has an open `spawned` entry recorded under another harness, or a live queen in another harness's `List`; the error names the sessions to `bs end`. Every project checks this on its own next command; bs does not scan other projects' logbooks.

## Workspace

The workspace is the directory a worker works in and the herdr workspace opened on it. Today that is always `herdr worktree create` on a git branch.

```go
package workspace

type Want struct{ Name, Project, Repo, Base, Label string } // Base: what to branch or fork from

type Made struct {
	ID, Pane, Dir string // herdr workspace id, its root pane, the working directory
	Branch        string // "" when the kind has no branch
	Data          map[string]string // kind-specific facts the spawn entry records (a client name)
}

type Workspace interface {
	Create(ctx context.Context, w Want, env map[string]string) (Made, error) // dir and herdr workspace together
	Remove(ctx context.Context, m Made, force bool) error                    // already gone → nil; ErrDirty without force
	Dirty(ctx context.Context, m Made) (bool, error)
	List(ctx context.Context) ([]Made, error) // workspaces this kind made, for orphans and ErrNotOurs
}
```

- **`git_worktree`** (default) keeps today's names: branch `<name>`, directory `<paths.worktrees>/<name>`. Because `herdr worktree create` has no `--env`, it runs `git worktree add` itself and opens the directory with `herdr workspace create --cwd --env --label --no-focus` (bees-12.5 decides, and live-tests the change).
- **`command`** runs the user's `create`, `remove` and `dirty` scripts with the name, base and project; `create` prints the absolute directory on stdout, and bs opens it with `herdr workspace create`. This is the CitC case.
- **`none`** opens a workspace on the main checkout. Only one live worker per project unless the config says otherwise.

herdr constraints the adapters encode (bees-12.4, herdr 0.9.5):

- `workspace create` with a missing `--cwd` exits 0 and opens in `$HOME`, so `Create` checks the directory exists first.
- `workspace list` shows no directory for a plain workspace, and the root pane's reported cwd is an early sample. So `Create` tags the workspace (`herdr workspace report-metadata <id> --source bs --token path=<dir>`), and `List` reads the tag. Orphans and `ErrNotOurs` are matched by that recorded path, never by label or by a live cwd that moves on `cd`.
- `workspace close` leaves the directory alone, and a second close fails with `workspace_not_found`, which `Remove` reads as "already gone".

## Delivery

Delivery is what "done" means and how bs proves a worker's work has landed.

```go
package delivery

type Verdict int // Landed | NotLanded | Unknown | NotChecked

type Delivery interface {
	Landed(ctx context.Context, sp logbook.Spawn) (Verdict, string, error) // the string says why
	Guards(defaultBranch string) guards.Rules                              // deny patterns and allow intents
	Prompt(role prompt.Role) string                                        // the role prompts' delivery fragment
}
```

- **`github_pr`** (default): `Landed` is today's three proofs (ancestry, a merged PR on the branch tip, identical paths: R42); `Guards` is today's push and merge matrix, against the repo's real default branch (bees-11.7); `Prompt` is today's PR wording, moved word for word out of `QUEEN.md` and `WORKER.md` (bees-12.3).
- **`manual`**: the worker reports `done: <bead>`, the keeper closes the bead, `Landed` is `NotChecked`, so `bs prune` deletes no branch and only reports what it skipped.
- **`command`**: a user script answers `Landed`; later, unless trivial.

`[guards] deny` is harness-neutral: shell-glob patterns over a command line. Its default is the delivery's `Guards` (plus the workspace's, if a kind needs any). Each harness renders the rules in its own syntax: Claude's `permissions.deny` and autoMode allow prose, omp's `bash.patterns`. Guards stay a speed bump (SPEC); branch protection, or the VCS's own review gate, is the real one.

## Naming (amends ADR-0006)

The name is still one forward function of the bead (`bees-12.3` → `bees-12-3`) and is the session name, the herdr agent name and the message address. It is the git branch and the `-WT` worktree directory only under `git_worktree`; `command` receives it and may use it (a client named after it), and `none` has neither. Whatever the kind makes is recorded in the `spawned` entry (`Made`), and teardown uses what was recorded, never a name rebuilt from config (motifs 3, 12). Project identity without git comes from `--project`/`BS_PROJECT` and `paths.repo` (bees-12.5).

## Security acceptance criteria (bees-12.2)

Every harness adapter, the claude_code extraction (bees-12.6) and the omp adapter (bees-12.8) included, must meet these before it ships. Same-uid processes can't be fenced from each other (bees-8.8), so the bar is: nothing another user or another machine can reach, and nothing weaker than Claude Code's own path.

- **SEC-1, ring authentication.** The ring endpoint is a unix socket mode 0600 in a directory mode 0700 (no TCP, no abstract socket), and a connection must present a random per-session token of at least 128 bits, kept in a 0600 file beside the registry entry; a connection without it is dropped without acting. bs never rings a bs-owned registry's session that has no token. Claude Code today (2.1.306, checked on this machine): sockets 0600 under `/run/user/<uid>/cc-socks` (0700), the registry `~/.claude/sessions` 0700, `.key` token files 0600; bs sends the token when the key file exists and rings without one otherwise.
- **SEC-2, no stale registry entry.** `List` and `Self` keep an entry only when its pid is alive and its start time, read by `proc.Start`, equals the one recorded at registration; anything else is skipped, never deleted on a guess. An unreadable registry is an error (motif 7). A bs-owned registry entry records the start time when the harness process writes it, and is removed on clean shutdown; a crash leaves an entry SEC-2 already ignores.
- **SEC-3, a trust step.** A launch either goes through the harness's own trust step (Claude's folder-trust prompt, which `Preflight` warns about), or disables the repo's ambient extensions, hooks and config (omp: `--trusted-extension <bs extension>`, plus a per-start `--config` overlay above the project layer). Deny rules sit in a layer the repo's own files can't override. A harness that can do neither is refused at spawn.
- **SEC-4, no secrets in bees.** API keys and OAuth tokens stay in the harness's own auth (omp login or its models file; agy's Google login). bs never writes them to config, the logbook, a pinned env var, a per-start file or herdr.

Test plan, run by the bead that builds each adapter:

| Id | Check | How |
|---|---|---|
| T1 | Claude drops a ring without the token, or doesn't | live-test: ring a toy worker over its socket with no auth line, then with a wrong token; record the outcome and, if it acts, make bs require the token for Claude too |
| T2 | the bs-owned socket refuses no token and a wrong token | unit test against the extension's listener; live-test with `socat` |
| T3 | modes 0700/0600 on the registry dir, socket and token file | unit test after `Prepare`, and a check in `bs doctor` (bees-12.9) |
| T4 | a reused pid with another start time names no one | fake registry entry with the live test process's pid and a wrong start time: `List`, `Self` and spawn's twin check all ignore it |
| T5 | an unreadable registry fails closed | registry dir mode 0000: spawn refuses, `bs status` errors, nothing starts |
| T6 | a repo's own hook can't run in or rewrite a worker | toy repo with an `.omp/hooks` (or the harness's equivalent) `tool_call` hook that writes a marker file and rewrites `git push` → spawn, push from the worker: no marker, and the deny rule still fires |
| T7 | a repo config can't loosen the guards | toy repo whose project config allows `git push origin main`: the push is still denied |
| T8 | no secret reaches bees | after a live run, grep the logbook, config, cache dir, per-start files and `/proc/<pid>/environ` of the pane shell for the key's prefix: none |

## Considered Options

- **A harness per role** (`queen.harness`, `worker.harness`; bees-11.11's first sketch). Rejected by the keeper: one harness per machine is enough for the north star, and it keeps one ring and identity path in each running fleet.
- **A `bs hello` the agent runs at start, so bs owns every registry.** Rejected as the registry write: a prompt step is not a process fact. Kept for the bs-owned registry's layout, which the harness's own extension writes.
- **Workspace and delivery as one VCS adapter.** Rejected: they vary apart. A git worktree can deliver by PR or by hand, and a CitC client delivers by CL, which a `command` delivery can check.
- **Keep env pinning per harness** (Claude's `--settings`, typed `export` lines for omp). Rejected once herdr's `--env` proved to reach the agent: one path, no shell typing, no launch-length cost.
- **Making git a fixed dependency, like herdr and bd.** Rejected: the CitC case is the north star.

## Consequences

- `internal/harness/claude` becomes the `claude_code` adapter behind `Harness`; the core packages stop importing it (bees-12.6). Existing tests must pass unchanged.
- The `spawned` entry's data gains `harness`, `workspace` (kind) and the kind's `Made` fields; `Branch` and `Worktree` become `git_worktree`'s, and old entries read as `claude_code` and `git_worktree`.
- `QUEEN.md` and `WORKER.md` lose their PR wording to `internal/prompt/delivery/<kind>.md`; the role prompts the bake-off replays are unchanged for `github_pr` (bees-12.3).
- `bs config`'s fixed header line about the only harness goes; the `harness` key now says it.
- A second harness is an adapter package plus, where the harness has no registry, an extension. That extension is bs-owned code that runs inside another program and has to meet SEC-1 to SEC-3.
