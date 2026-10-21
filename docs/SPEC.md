# bs spec

`bs` lets coding-agent sessions (Claude Code today; [ADR-0009](adr/0009-core-and-adapters.md)) in herdr panes send each other messages through a shared logbook, and wakes an idle session when it has mail. Vocabulary is in [GLOSSARY.md](../GLOSSARY.md); the reasons behind the shape are in [docs/adr/](adr/); every decision is indexed in [DECISIONS.md](DECISIONS.md).

This spec covers **v0**, the walking skeleton, plus **spawn**, which v0 grows into for dogfooding. [v1](#v1-outline) is outlined at the end and gets its own spec pass.

## Scope

v0 proves the core loop: the queen sends a message, an idle worker wakes, reads it and replies, and the queen wakes. Spawn then lets the queen start a worker per bead in its own herdr workspace ([ADR-0006](adr/0006-spawn-and-naming.md)).

| In v0 + spawn | Deferred to v1 |
|---|---|
| `bs init`, `send`, `inbox`, `status`, `queen`, `spawn`, `end` | `bs hook` |
| `entries` table; writes `message`, `received`, `spawned`, `ended` | `rang` entries |
| Claude adapter: find, ring; herdr and bd adapters | reconciler |
| Ring every unread message on every send | `Plan` reconciler, coalescing, re-ring, flag the queen, watchdog |
| `--now` | Keeper inbox, disk warning, more harnesses (ADR-0009) |

Platform: Linux and macOS (R47).

## Done when

1. An idle worker wakes within 2 s of `bs send`.
2. A queen → worker → queen round trip completes with nobody typing in either session.
3. Messages sent while a worker is not running are delivered on the first send after it starts.
4. A bs process killed mid-write loses no committed message and leaves the logbook readable.

## Layout

```text
cmd/bs/                   subcommands, flags, output
internal/logbook/          the entries table
internal/project/          which project, logbook and repo; which session am I
internal/worker/           Fleet.Spawn / Fleet.End; Herdr and Beads ports with CLI adapters
internal/harness/claude/   registry lookup, auth token, NDJSON ring
```

## Logbook

The logbook is a SQLite file in WAL mode ([ADR-0005](adr/0005-sqlite-not-dolt.md)), opened through `modernc.org/sqlite`.

```text
BS_LOGBOOK  default  <paths.logbook_dir>/<project>.db   (one logbook per project, R23; ADR-0008)
pragmas      journal_mode=WAL, busy_timeout=5000, foreign_keys=ON; writes use BEGIN IMMEDIATE
```

```sql
CREATE TABLE IF NOT EXISTS entries (
  id      TEXT PRIMARY KEY,           -- time-prefixed random id; receipts: 'rcv:<msg id>:<session>'
  ts      TEXT NOT NULL,              -- RFC 3339, UTC, milliseconds
  kind    TEXT NOT NULL CHECK (kind IN ('message','received','rang','spawned','ended')),
  actor   TEXT NOT NULL,              -- session that wrote the entry
  subject TEXT,                       -- message: recipient; spawned/ended: worker name
  ref     TEXT REFERENCES entries(id), -- received: the message; ended: the spawned entry; a brief: its spawned entry
  data    TEXT CHECK (data IS NULL OR json_valid(data)) -- message: {"body": "..."}
) STRICT;
CREATE INDEX IF NOT EXISTS k_subj ON entries (kind, subject, id);
CREATE INDEX IF NOT EXISTS k_ref  ON entries (kind, ref);
```

Rules:

- Entries are only inserted, never updated or deleted.
- Each write is one `BEGIN IMMEDIATE` transaction; concurrent writers wait on the busy timeout instead of failing.
- A receipt id is deterministic and written with `INSERT OR IGNORE`, so a duplicate is a no-op.
- `Open` creates the file, table and indexes if missing. Only `bs init`, `queen` and `spawn` call it on a missing file, and say so on stderr; every other command refuses one (exit 2), so a deleted logbook is never silently recreated empty (R48).
- Unread is always this query, in insertion order (`rowid`). Never use a cursor or a time window.

```sql
SELECT m.id, m.actor, m.ts, m.data FROM entries m
WHERE m.kind = 'message' AND m.subject = ?
  AND NOT EXISTS (SELECT 1 FROM entries r
                  WHERE r.kind = 'received' AND r.ref = m.id AND r.actor = ?)
  AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = m.ref)
ORDER BY m.rowid;
```

The last clause retires a brief whose spawn has ended, so a worker spawned again for the bead gets only its own brief (R39).

```go
package logbook

type ID string
type Message struct {
	ID       ID
	From, To string
	Body     string
	TS       time.Time
}

func Open(ctx context.Context, path string) (*Logbook, error) // creates file, dir and schema if missing
func (l *Logbook) Send(ctx context.Context, from, to, body string) (ID, error)
func (l *Logbook) Unread(ctx context.Context, session string) ([]Message, error)
func (l *Logbook) RecordReceipts(ctx context.Context, session string, ids []ID) error // one commit
func (l *Logbook) RecordSpawn(ctx context.Context, by string, s Spawn) (ID, error)
func (l *Logbook) RecordEnd(ctx context.Context, by string, spawn ID) error
func (l *Logbook) LatestSpawn(ctx context.Context, name string) (Spawn, bool, error) // Spawn.Ended set from a matching ended entry
func (l *Logbook) UnreadCounts(ctx context.Context) (map[string]int, error)          // every session the logbook names → unread (0 if none)
```

## Claude adapter

```go
package claude

type Priority string // "now" | "next"

type Session struct {
	Name      string
	PID       int
	ProcStart string // /proc start time in clock ticks, as Claude records it
	Socket    string
	Token     string // empty when the session has no key file
}

func AtPID(pid int, name string) (Session, error) // the recorded pid's entry only; ErrNotRunning when it exited
func Ring(s Session, text string, p Priority) error
```

`AtPID` reads only `~/.claude/sessions/<pid>.json` for the pid an entry recorded, and returns the session only when all of these hold (else an error; `ErrNotRunning` when the process exited):

- `peerProtocol == 1`;
- `/proc/<pid>` exists and its start time equals `procStart`;
- its `name` is the session rung;
- `messagingSocketPath` is set.

The token comes from `<pid>.<sha256(socket path)>.key` (`peerToken`).

`Ring` connects to the unix socket and writes NDJSON. The auth line is sent only when there is a token. Then it half-closes; there is no reply.

```json
{"type":"auth","token":"<peerToken>"}
{"type":"user","message":{"role":"user","content":"<text>"},"priority":"next","msg_id":"<uuid v4>"}
```

A receiving session must have `"crossSessionInbound": "accept"` in its settings. Otherwise a session in bypass mode holds peer messages instead of acting on them.

## Project and identity

```go
package project

type Project struct{ Name, Repo, Logbook string }
func Resolve(flag string) (Project, error) // --project → BS_PROJECT → basename of git's common dir
func (p Project) Queen() string             // "queen-" + Name
func Me(flagAs string) (string, error)      // --as → parent-process walk → BS_SESSION
```

- The name is lowercased with `-` and `_` dropped (`pymatgen-analysis-defects` → `pymatgenanalysisdefects`) and must match `^[a-z][a-z0-9]{0,24}$`, else `ErrBadProject` (set `BS_PROJECT`).
- The project comes from git's *common* directory, so a shell inside `<repo>-WT/bees-2-1` resolves to the main repo's project.
- `Repo` is the first of the repo chain (ADR-0008): `--repo`, the main checkout when you are in the project's git repo, the project file's `paths.repo`, else the directory under `paths.repos` (`$BS_REPOS`) whose normalised name is the project, so `jobflowray` finds `jobflow-ray`, else empty: messages need only the logbook, and `bs queen` and `bs spawn` fail with `ErrNoRepos` (set `BS_REPOS`). A `--repo` or `paths.repo` that is a linked worktree is the `Base` workers branch from, while `Repo` is its main checkout; worktrees go under `paths.worktrees` (`{repo}-WT`); `Logbook` is `$BS_LOGBOOK`, else `<paths.logbook_dir>/<project>.db` (`$BS_LOGBOOK_DIR`, else the global config file, else `~/.local/share/bees`: ADR-0008; `paths.logbook_dir` is global only). Inside a herdr pane that an open `spawned` entry or a queen's newest `queen` entry recorded, with its process alive and an ancestor of the calling bs, the pane decides instead: `internal/identity` scans `<global paths.logbook_dir>/*.db` read only, and the logbook that recorded the pane gives the project, the name and the logbook; an inherited `BS_PROJECT`, `BS_LOGBOOK`, `BS_LOGBOOK_DIR` or `BS_SESSION` that disagrees is ignored with a warning (ADR-0009 amendment 1). `bs queen` and `bs spawn` refuse (exit 2) a logbook outside the global `paths.logbook_dir`, where no pane scan looks.
- Role prompts (R40): `internal/prompt/QUEEN.md` for the queen and `WORKER.md` for workers, each followed by `BEADS.md` (the bead rules both share), embedded in bs with `{{project}}` filled in by `prompt.Text(role, project)`. `prompt.File` writes one to `<user cache dir>/bees/<project>-<role>.md` (`~/.cache/bees/<project>-queen.md`, `…-worker.md`; through a rename, at every launch), passed as `--append-system-prompt-file` to the queen and to every worker, `--repo` ones too (R46). A file, not the text, because herdr refuses agent arguments containing control characters (R39). They are the one place the standing instructions for each role live: the queen hands out beads, reads the fleet from `bs status`, and closes a bead only after its PR is merged; a worker reads its brief and inbox, asks the queen when blocked, and ends on a PR and `bs done --pr <url>`; both list and file beads with `bs bead`, which keeps them in the project (R32). Claude records the prompt with the conversation until compaction, so restart a queen with `bs queen`, not a bare `claude --resume`.
- In a recorded pane, `Me` is the pane's session and an `--as` naming anyone else is refused; `send --project <other>` still works, its message recording `from_project`, so the recipient's `bs inbox` prints the reply command; `inbox --project <other>` is refused. Outside one, `Me` walks `getppid()` upward to the first pid with `~/.claude/sessions/<pid>.json` and uses its `name`. The registry beats `BS_SESSION`, which a session can inherit stale; `BS_SESSION` is for use outside Claude. Otherwise it fails with `run bs from inside a named Claude session, or pass --as`. Refusing an `--as` that names another session stops a careless one, not a determined one: a bs detached from its session (`( bs … & )`, `nohup`, `setsid`) is reparented to init, the walk finds no Claude ancestor and `--as` is taken as given. Claude's own background Bash stays under claude. Same-uid processes can't be fenced from each other, and any of them can write the logbook directly (bees-8.8).

## Spawn

```go
package worker

func Name(bead string) (string, error) // bees-12.3 → bees-12-3; refuses names herdr can't take
func Belongs(project, bead string) bool // ^<project>-\d+(\.\d+)*$
func NextEpic(project string, ids []string) string // <project>-<max+1>

type Herdr interface {
	CreateWorktree(ctx context.Context, repo, branch, path, label string) (Workspace, error)
	StartAgent(ctx context.Context, pane, name, kind string, args ...string) error // ErrWaiting, ErrUnconfirmed, ErrUnsafeArg
	RemoveWorktree(ctx context.Context, workspace string) error               // already gone → nil
	Workspaces(ctx context.Context) ([]Workspace, error)
	ProcessInfo(ctx context.Context, pane string) (PaneProcesses, error) // at start only; ErrPaneGone
	RenameAgent(ctx context.Context, pane, name string) error             // a label and a launch fence, never read back
}
type Procs interface { // the process table: SystemProcs (internal/proc)
	Alive(p proc.Process) (bool, error) // pid alive with its recorded start time
	Start(pid int) (string, error)
	Name(pid int) (string, error)
	Exe(pid int) (string, error)
	Descendants(pid int) ([]proc.Process, error)
	Parent(pid int) (int, error)
	Command(pid int) (string, error)                                   // the command line
	Listening(ctx context.Context, pids []int) (map[int][]int, error) // TCP ports each listens on
}
type Beads interface{ Show(ctx context.Context, id string) (Bead, error) }

func CheckAgentArgs(args []string) error // ErrUnsafeArg: a Unicode control character, which herdr refuses

type Fleet struct {
	P  project.Project
	LB *logbook.Logbook
	H  Herdr
	Procs Procs
	B  Beads
	Me string
}
type Started struct {
	Name, Workspace string
	Resumed         bool // restarted in its recorded workspace
	Continued       bool // ... with claude --continue, because it had read its inbox since this spawn
	Waiting         bool // herdr said agent_not_ready: Claude is blocked at a prompt (folder trust)
}

func (f Fleet) Spawn(ctx context.Context, bead, label string) (Started, error)       // ErrRunning, ErrWrongRepo, ErrOrphan, ErrNoLabel, ErrBadLabel, ErrLabelChange

type Request struct{ Bead, Label string }
type Result struct {
	Bead    string
	Started Started
	Err     error
	Took    time.Duration
}
// SpawnAll: one Result per Request, in order. Every request is checked (steps 1–5) before
// anything is touched; if any is Refused, nothing starts and the rest get ErrNotStarted.
// Then steps 6–7 run one worker at a time and step 8 runs concurrently; past the checks, a
// failure doesn't stop the others. A worker named twice is ErrTwice the second time (a
// refused first occurrence doesn't count).
func (f Fleet) SpawnAll(ctx context.Context, reqs []Request) []Result
func Refused(err error) bool // ErrBadLabel, ErrNoLabel, ErrLabelChange, ErrTwice: fix the command
func (f Fleet) End(ctx context.Context, name string) error                       // ErrNotSpawned; already ended → nil
func (f Fleet) Prune(ctx context.Context, dry bool) ([]Pruned, error)           // one Pruned per ended worker's branch still present
```

`Spawn(bead)`:

1. `name = Name(bead)`.
2. If `LatestSpawn(name)` is open:
   - the process its newest `started` entry recorded is alive with its start time → `ErrRunning` (a spawn with no `started` entry reads as not running; its start then refuses if the harness already runs in its pane, so no twin starts beside an unrecorded one); a lookup that fails is an error, never "not running" (no twins, ADR-0009);
   - otherwise → restart at step 7 in the recorded workspace. If the worker has read a message since this spawn (`Spawn.Read`), add `--continue` so it keeps its conversation; if not, its first start never got going, and `claude --continue` would exit with "No conversation found to continue", so it starts fresh with the doorbell.
3. The bead id must be `<project>-<n>[.<m>…]` (`Belongs`), unless `--repo` → else `ErrWrongRepo`.
4. A first spawn (no live `spawned` entry) needs a label (`ErrNoLabel`); a restart reuses the recorded one and refuses another (`ErrLabelChange`). A label is 1–4 words of `[a-z0-9]`, single spaces, ≤ 32 characters (`CheckLabel`, else `ErrBadLabel`; never rewritten). The queen writes it from the bead; herdr itself stores any label verbatim (`TestHerdrKeepsLabelsVerbatim`).
5. A herdr workspace whose worktree `checkout_path` is `<paths.worktrees>/<name>`, or the worktree the name's latest (ended) spawn recorded, but no live `spawned` entry → `ErrOrphan`. Found by path, never by label: the label is free text, the path is built forward from the name, with the command to remove it by hand (`herdr worktree remove --workspace <id> --force`). It is never removed automatically.
6. `herdr worktree create --cwd <repo> --branch <name> --path <paths.worktrees>/<name> --label <label> --no-focus [--base <sha>]` (herdr prints JSON). `--cwd` is always the main checkout: herdr 0.9.5 refuses a linked worktree there ("New and open worktree actions start from the repo parent workspace"). When `--repo`/`paths.repo` named a linked worktree, `--base` is its `HEAD`.
7. `RecordSpawn` immediately, in one transaction with the brief (`bd show` title and description) as a message from the queen: subject = name; data = bead, label, workspace, pane, worktree, branch, repo, base (the checkout it was cut from).
8. `herdr agent start <name> --kind claude --pane <pane> --timeout 10000 -- --name <name> --settings '{"crossSessionInbound":"accept",…}' --append-system-prompt-file ~/.cache/bees/<project>-worker.md "<doorbell>"`. No env is pinned: a worker in another repo (`--repo`), or one whose pane inherits another `BS_LOGBOOK`, finds the spawner's project and logbook from its pane, which the spawner's logbook recorded (ADR-0009 amendment 1). With `--path`, the settings also carry a `SessionStart` hook that appends `export PATH="<dirs>:$PATH"` to `$CLAUDE_ENV_FILE`, which Claude's Bash tool sources after the shell profile (bees-8.19). Every argument passes `CheckAgentArgs` first. The doorbell is Claude's first prompt, so spawn doesn't depend on ringing. herdr's outcomes ([ADR-0007](adr/0007-claudes-registry-confirms-a-launch.md)):
   - success → started;
   - `agent_not_ready`: Claude is blocked at a prompt → started with `Waiting`; bs tells the keeper to answer it in the worker's pane;
   - `timeout` → `ErrUnconfirmed`. herdr waits for an idle prompt, and a worker busy with its doorbell is never idle in time, so this is the usual outcome for a healthy worker; Claude may also have exited. Whatever herdr says, bs confirms the start itself: from before the launch until 20 s, it polls `herdr pane process-info` for a process named the harness's kind (in the foreground, else below the pane's shell). Found → a `started` entry with its pid, start time, executable and baseline descendants, then `herdr agent rename <pane> <name>`; not → `ErrUnconfirmed`, an error naming `herdr pane read <pane>` (ADR-0009 amendment 1). Before spawning, `bs spawn` warns if `~/.claude.json` shows the repo isn't trusted yet (read only; trust is keyed by the main checkout, so its worktrees share it). No settings file is written (Claude reads `settings.local.json` from the main checkout in a worktree), and no `BS_*` variable is set (ADR-0009 amendment 1).

`End(name, force)`: read `LatestSpawn`; none → `ErrNotSpawned` (never ask herdr which workspace). Without force, the background-work gate (ADR-0009 amendment 1) comes first: a live descendant of the newest start's process that its baseline doesn't hold, that isn't this bs or its ancestors, and that is still there 1 s later → `*BackgroundError` (exit 5); any failure to look → `ErrCantCheck` (exit 6); a recorded process that has exited, or a start never recorded whose pane runs no harness, passes. Otherwise find the recorded workspace id in `herdr workspace list`: open on another path → `ErrNotOurs`, nothing removed; gone → just `RecordEnd`; else `herdr worktree remove --workspace <recorded id>` (`--force` only with `bs end --force`; a dirty worktree → `ErrDirty`, nothing ended), then `RecordEnd`. The branch is always kept: `bs end` prints `branch <name> kept; bs prune deletes it once merged`.

`Prune(dry)`: for each worker whose latest spawn has ended (`EndedSpawns`; only branches the logbook recorded, never guessed from git) and whose branch still exists in the recorded repo, decide `Merged` / `NotMerged` / `Unknown`, in order: the branch is an ancestor of the default branch; GitHub (`GitHub.MergedHeads`, `gh pr list --head <b> --state merged`, bounded at 15 s) has a merged PR whose head is the branch tip (gh missing, offline or no remote → skipped and noted, never "merged"); every path the branch changed since its merge-base is identical on the default branch (only those paths: main may have moved on; a branch that changed no paths isn't proven). The default branch is `origin/HEAD`'s target and its local twin, else `main`. A probe error is `Unknown`. `Merged` → `git branch -D` unless `dry`. Remote branches are never touched.

## Commands

```text
bs init                       create the logbook file and table if missing; bs queen and spawn do too, any other command refuses a missing logbook (exit 2)
bs queen                      start the queen: claude --name queen-<project> --append-system-prompt-file <project>-queen.md in the repo (no BS_* set)
bs spawn [--repo P] [--path D …] --label L <bead> [--label L <bead> …]  start a worker per bead (SpawnAll), each in its own herdr workspace labelled by the --label before it (--label only on a first spawn; --repo and --path before the first bead, for all; --path puts an absolute dir first on each worker's shell PATH, recorded in its spawned entry and reused on a restart, e.g. a live test's build: bees-8.19); exit 2 if refused (nothing started), 1 if any start failed
bs end [--force] <worker|bead>  remove the worker's recorded workspace; keep its branch; exit 5 while it runs background work, 6 if bs can't check (--force overrides both)
bs prune [--dry-run]          delete ended workers' local branches once merged; exit 3 if one is kept unmerged or checked out in a worktree, 4 if one couldn't be checked or deleted
bs epic <title>               create the project's next epic, <project>-<n>
bs bead ls [--all] [<expr>]   bd query 'id="<project>-*"', not closed unless --all; <expr> is ANDed
bs bead new --parent <project>-N [--description D] [--type T] [--priority P] <title>
                              bd create --force under that parent; prints the new id
bs send <to> [--now] <body|-> store a message, then ring <to>   (flags may follow <to>)
bs inbox [--peek]             print unread messages and done entries; without --peek, record receipts in one commit
bs done (--pr U | --report T|-) [--force]  a worker, in its recorded pane, reports its bead done to its queen: a done entry, rung like a send; exit 5 while it runs background work (--force records it in the entry), 6 if bs can't check
bs status [--json]            each known session: running or not, unread count, a live worker's newest done
bs config [--project p]       every setting from $XDG_CONFIG_HOME/bees as key = "value"  # source (ADR-0008); exit 1 on a config error
bs version                    the commit bs was built from
send, inbox, spawn, end        --project, --as     (init, queen, epic, bead, config: --project)
```

Run inside bs's own source repo, every command warns on stderr when bs was built from a commit strictly behind that repo's `main` (R39).

`bs send`:

```go
id := lb.Send(ctx, me, to, body)
fmt.Printf("sent %s to %s\n", id, to)
t, err := target(to) // the queen's newest queen entry, else an open spawn's newest start: alive with its start time
if errors.Is(err, harness.ErrNotRunning) {
	fmt.Printf("queued; %s is not running\n", to)
	return nil
}
s, err := claude.AtPID(t.PID, to) // only <pid>.json; fails closed (ADR-0009 SEC-2)
unread := lb.Unread(ctx, to)
if err := claude.Ring(s, doorbell(unread, me), prio); err != nil {
	fmt.Fprintf(os.Stderr, "couldn't ring %s (the message is stored): %v\n", to, err) // still exit 0
}
```

The doorbell text explains itself, so workers need no setup prompt in v0:

```text
[bs] 2 unread (queen ×2) — run: bs inbox   (reply with: bs send queen "...")
[bs] 2 unread (bees-1-3, bees-1-2) — run: bs inbox   (reply with: bs send bees-1-2 "...")
```

Senders are listed in order of their oldest unread message; "reply with" names whoever just sent.

`bs status` combines the recorded processes (a worker's newest `started` entry, the newest `queen` entry) with `lb.UnreadCounts()`; herdr isn't asked. A session whose process can't be looked up shows as `unknown`, and `bs status` exits 1. It shows the sessions this project's logbook names (writers, recipients, workers) that are running or have unread messages, with their unread counts. A stopped session with nothing unread is history and is left out. Claude's registry is machine-wide, so a running session this project's logbook has never named (another project's, or one that hasn't used bs yet) is left out too (R30). It writes nothing.

```text
SESSION      STATE        UNREAD
queen-bees  running      0
bees-1-4    not running  2
```

`--json` prints `[{"session": "...", "running": true, "unread": 0}, ...]`, sorted by session.

## Setup

Full guide: [SETUP.md](SETUP.md).

```sh
bs queen                 # queen-<project>, in $BS_REPOS/<project>
bs epic "ringing"        # → bees-2; then bs bead new --parent bees-2 "…"
bs spawn --label "ring on send" bees-2.1   # inside the queen: bees-2-1 in bees-WT/bees-2-1
```

Manual worker (no herdr): `claude --name worker-a --settings '{"crossSessionInbound":"accept"}'` in its worktree.

## Tests

| Test | Checks |
|---|---|
| `logbook` tests | each test opens a fresh logbook file in `t.TempDir()` |
| `TestSendUnreadMarkRead` | round trip |
| `TestConcurrentSends` | 20 separate processes or connections sending at once; no loss, no busy errors |
| `TestUnreadIsSet` | reading a newer message leaves older ones unread |
| `TestRecordReceiptsIdempotent` | recording the same receipt twice is a no-op |
| `project` tests | resolution order; a linked worktree resolves to its main repo's project |
| `worker` tests (fake Herdr and Beads) | spawn → settings + entry + brief + start args; second spawn `ErrRunning`; dead agent resumes in the same workspace with `--continue` only if it read its inbox since the spawn; `agent_not_ready` counts as started; herdr `timeout` counts as started only with a live registry session; an unreadable registry starts nothing; an ended spawn's brief is retired; first spawn without a label or with a bad one refused before herdr; a restart keeps its label and refuses another; orphan (by worktree path, not label) `ErrOrphan` with the cleanup command and nothing removed; end removes exactly the recorded id; wrong repo label refused |
| `TestHerdrKeepsLabelsVerbatim` | herdr stores any workspace label as given (empty, newlines, 200 characters, unicode), so fakeHerdr does too and `CheckLabel` is bs's rule; opens and closes a throwaway workspace per label; skipped without a running herdr |
| `TestCheckAgentArgsMatchesRealHerdr` | the probed table of agent arguments against the installed herdr (unknown pane, so nothing starts); skipped without a running herdr. Every constraint fakeHerdr enforces comes from `CheckAgentArgs`, so this holds the fake to the real tool |
| `claude` tests (`claudetest.FakeRegistry`) | a fake unix listener sees the auth line only when a key exists, then the user line with its priority, then EOF; a `procStart` mismatch or another `peerProtocol` gives `ErrNotRunning` |
| `cmd/bs` status tests | running with 0 and N unread, not running with N unread, stopped with none left out; `--json`; status records no receipts |
| `cmd/bs` ring tests | doorbell text and `--now`; not running → stored, "queued", exit 0; failed ring → warning, exit 0 |
| `TestKilledWriterLosesNoCommittedMessage` | "Done when" 4: a separate process SIGKILLed mid-way through sends; every acknowledged send survives and the logbook still writes |
| `scripts/e2e.sh <bead>` | "Done when" 1–3 with a real queen and worker in herdr: wake time from the worker's transcript, round trip, mail queued while stopped |

## v1 outline

- **Reconciler** ([ADR-0003](adr/0003-reconcile-on-triggers-not-a-daemon.md)):
  - `Plan(Facts, now) []Action`, run from send, hooks (5 s debounce), status, inbox and a 60 s watchdog;
  - one reconcile at a time, serialized by SQLite's write lock;
  - one doorbell in flight per session, re-ring at 60 s, flag the queen at 120 s.
- **Hook:** `bs hook` on UserPromptSubmit writes a `rang`/landed entry for `[bs]` prompts; hooks and herdr agent status are the attention facts for `Plan`, never screen scraping (R38).
- **Questions and answers** (R35, R36): message `data` gains `bead` and `needs` (`none` | `answer` | `approval`); `bs send --needs … --bead … --re <id>`; an answer is a message whose `ref` is the question, and the first one wins; `bs status` counts open questions.
- **Keeper** (R13, R37): a message to `keeper` with `needs` set is something that needs the human; `bs status` lists them first. Desk only, no push (S3).
- **Harness port** ([ADR-0009](adr/0009-core-and-adapters.md)): `Kind`, `Prepare`, `Ring`, superseding `{Launch, Ring, Alive}`; liveness and identity from the recorded herdr pane; `[workspace]` and `[delivery]` are config switches, `[guards]` rendered per harness.
