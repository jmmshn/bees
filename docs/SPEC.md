# bs spec

`bs` lets Claude Code sessions in herdr panes send each other messages through a shared logbook, and wakes an idle session when it has mail. Vocabulary is in [GLOSSARY.md](../GLOSSARY.md); the reasons behind the shape are in [docs/adr/](adr/); every decision is indexed in [DECISIONS.md](DECISIONS.md).

This spec covers **v0**, the walking skeleton, plus **spawn**, which v0 grows into for dogfooding. [v1](#v1-outline) is outlined at the end and gets its own spec pass.

## Scope

v0 proves the core loop: the queen sends a message, an idle bee wakes, reads it and replies, and the queen wakes. Spawn then lets the queen start a bee per bead in its own herdr workspace ([ADR-0006](adr/0006-spawn-and-naming.md)).

| In v0 + spawn | Deferred to v1 |
|---|---|
| `bs init`, `send`, `inbox`, `status`, `queen`, `spawn`, `end` | `bs hook` |
| `entries` table; writes `message`, `received`, `spawned`, `ended` | `rang` entries |
| Claude adapter: find, ring; herdr and bd adapters | reconciler |
| Ring every unread message on every send | `Plan` reconciler, coalescing, re-ring, flag the queen, watchdog |
| `--now` | Captain inbox, disk warning, omp adapter |

Platform: Linux only.

## Done when

1. An idle bee wakes within 2 s of `bs send`.
2. A queen → bee → queen round trip completes with nobody typing in either session.
3. Messages sent while a bee is not running are delivered on the first send after it starts.
4. A bs process killed mid-write loses no committed message and leaves the logbook readable.

## Layout

```text
cmd/bs/                   subcommands, flags, output
internal/logbook/          the entries table
internal/project/          which project, logbook and repo; which session am I
internal/bee/             Fleet.Spawn / Fleet.End; Herdr and Beads ports with CLI adapters
internal/harness/claude/   registry lookup, auth token, NDJSON ring
```

## Logbook

The logbook is a SQLite file in WAL mode ([ADR-0005](adr/0005-sqlite-not-dolt.md)), opened through `modernc.org/sqlite`.

```text
BS_LOGBOOK  default  ~/.local/share/bees/<project>.db   (one logbook per project, R23)
pragmas      journal_mode=WAL, busy_timeout=5000, foreign_keys=ON; writes use BEGIN IMMEDIATE
```

```sql
CREATE TABLE IF NOT EXISTS entries (
  id      TEXT PRIMARY KEY,           -- time-prefixed random id; receipts: 'rcv:<msg id>:<session>'
  ts      TEXT NOT NULL,              -- RFC 3339, UTC, milliseconds
  kind    TEXT NOT NULL CHECK (kind IN ('message','received','rang','spawned','ended')),
  actor   TEXT NOT NULL,              -- session that wrote the entry
  subject TEXT,                       -- message: recipient; spawned/ended: bee name
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
- `Open` creates the table and indexes if missing, so `bs init` is optional.
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

func List() ([]Session, error)        // every live, compatible session
func Find(name string) (Session, error) // newest of that name; ErrNotRunning when absent or stale
func Ring(s Session, text string, p Priority) error
```

`List` reads `~/.claude/sessions/<pid>.json` and keeps a session only when all of these hold:

- `peerProtocol == 1`;
- `/proc/<pid>` exists and its start time equals `procStart`;
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
- `Repo` is the main checkout when the project comes from git, else the directory under `$BS_REPOS` (default `~/repos/jmmshn`) whose normalised name is the project, so `jobflowray` finds `jobflow-ray`; `Logbook` is `$BS_LOGBOOK`, else `~/.local/share/bees/<project>.db`.
- `internal/prompt/system.md`, embedded in bs with `{{project}}` filled in by `prompt.System`, is written by `prompt.File` to `<user cache dir>/bees/<project>-system.md` (`~/.cache/bees/`, through a rename, at every launch) and passed as `--append-system-prompt-file` to the queen and to every worker whose repo is the project's. A file, not the text, because herdr refuses agent arguments containing control characters (R39). It is the one place the instructions for queens and bees live; today it tells them to list and file beads with `bs bead`, which keeps them in the project (R32). Claude records it with the conversation until compaction, so restart a queen with `bs queen`, not a bare `claude --resume`.
- `Me` walks `getppid()` upward to the first pid with `~/.claude/sessions/<pid>.json` and uses its `name`. The registry beats `BS_SESSION`, which a session can inherit stale; `BS_SESSION` is for use outside Claude. Otherwise it fails with `run bs from inside a named Claude session, or pass --as`.

## Spawn

```go
package bee

func Name(bead string) (string, error) // bees-12.3 → bees-12-3; refuses names herdr can't take
func Belongs(project, bead string) bool // ^<project>-\d+(\.\d+)*$
func NextEpic(project string, ids []string) string // <project>-<max+1>

type Herdr interface {
	CreateWorktree(ctx context.Context, repo, branch, path, label string) (Workspace, error)
	StartClaude(ctx context.Context, pane, name string, args ...string) error // ErrWaiting, ErrUnconfirmed, ErrUnsafeArg
	RemoveWorktree(ctx context.Context, workspace string) error               // already gone → nil
	Workspaces(ctx context.Context) ([]Workspace, error)
}
type Sessions interface { // Claude's registry: ClaudeSessions
	Alive(ctx context.Context, name string, wait time.Duration) (bool, error)
}
type Beads interface{ Show(ctx context.Context, id string) (Bead, error) }

func CheckAgentArgs(args []string) error // ErrUnsafeArg: a Unicode control character, which herdr refuses

type Fleet struct {
	P  project.Project
	LB *logbook.Logbook
	H  Herdr
	S  Sessions
	B  Beads
	Me string
}
type Started struct {
	Name, Workspace string
	Resumed         bool // restarted in its recorded workspace
	Continued       bool // ... with claude --continue, because it had read its inbox since this spawn
	Waiting         bool // herdr said agent_not_ready: Claude is blocked at a prompt (folder trust)
}

func (f Fleet) Spawn(ctx context.Context, bead string) (Started, error)              // ErrRunning, ErrWrongRepo, ErrOrphan
func (f Fleet) End(ctx context.Context, name string) (branchKept bool, err error) // ErrNotSpawned; already ended → nil
```

`Spawn(bead)`:

1. `name = Name(bead)`.
2. If `LatestSpawn(name)` is open:
   - Claude's registry has a live session called name → `ErrRunning`; a registry that can't be read is an error, never "not running" (no twins);
   - otherwise → restart at step 7 in the recorded workspace. If the worker has read a message since this spawn (`Spawn.Read`), or runs in another project's repo (its receipts are in that project's logbook), add `--continue` so it keeps its conversation; if not, its first start never got going, and `claude --continue` would exit with "No conversation found to continue", so it starts fresh with the doorbell.
3. The bead id must be `<project>-<n>[.<m>…]` (`Belongs`), unless `--repo` → else `ErrWrongRepo`.
4. A herdr workspace labelled with the bead but no `spawned` entry → `ErrOrphan`, with the command to remove it by hand (`herdr worktree remove --workspace <id> --force`). It is never removed automatically.
5. `herdr worktree create --cwd <repo> --branch <name> --path <repo>-WT/<name> --label <bead> --no-focus` (herdr prints JSON).
6. `RecordSpawn` immediately, in one transaction with the brief (`bd show` title and description) as a message from the queen: subject = name; data = bead, workspace, pane, worktree, branch, repo.
7. `herdr agent start <name> --kind claude --pane <pane> --timeout 10000 -- --name <name> --settings '{"crossSessionInbound":"accept"}' --append-system-prompt-file ~/.cache/bees/<project>-system.md "<doorbell>"` (no system prompt when the bee runs in another project's repo). Every argument passes `CheckAgentArgs` first. The doorbell is Claude's first prompt, so spawn doesn't depend on ringing. herdr's outcomes ([ADR-0007](adr/0007-claudes-registry-confirms-a-launch.md)):
   - success → started;
   - `agent_not_ready`: Claude is blocked at a prompt → started with `Waiting`; bs tells the captain to answer it in the worker's pane;
   - `timeout` → `ErrUnconfirmed`. herdr waits for an idle prompt, and a worker busy with its doorbell is never idle in time, so this is the usual outcome for a healthy worker; Claude may also have exited. `Sessions.Alive(name, 20s)` decides: live → started; not → an error naming `herdr pane read <pane>`. Before spawning, `bs spawn` warns if `~/.claude.json` shows the repo isn't trusted yet (read only; trust is keyed by the main checkout, so its worktrees share it). No settings file is written (Claude reads `settings.local.json` from the main checkout in a worktree), and no `BS_*` variables are needed.

`End(name)`: read `LatestSpawn`; none → `ErrNotSpawned` (never ask herdr). Otherwise `herdr worktree remove --workspace <recorded id> --force`, then `git -C <recorded repo> branch -d <name>` (kept, and reported, if not fully merged), then `RecordEnd`.

## Commands

```text
bs init                       create the logbook file and table if missing (optional)
bs queen                      start the queen: claude --name queen-<project> --append-system-prompt-file <system.md> in the repo (no BS_* set)
bs spawn <bead> [--repo P]    start a bee for the bead in its own herdr workspace
bs end <bee>                 remove the bee's recorded workspace
bs epic <title>               create the project's next epic, <project>-<n>
bs bead ls [--all] [<expr>]   bd query 'id="<project>-*"', not closed unless --all; <expr> is ANDed
bs bead new --parent <project>-N [--description D] [--type T] [--priority P] <title>
                              bd create --force under that parent; prints the new id
bs send <to> [--now] <body|-> store a message, then ring <to>   (flags may follow <to>)
bs inbox [--peek]             print unread; without --peek, record receipts in one commit
bs status [--json]            each known session: running or not, unread count
bs version                    the commit bs was built from
send, inbox, spawn, end        --project, --as     (init, queen, epic, bead: --project)
```

Run inside bs's own source repo, every command warns on stderr when bs was built from a commit strictly behind that repo's `main` (R39).

`bs send`:

```go
id := lb.Send(ctx, me, to, body)
fmt.Printf("sent %s to %s\n", id, to)
s, err := claude.Find(to)
if errors.Is(err, claude.ErrNotRunning) {
	fmt.Printf("queued; %s is not running\n", to)
	return nil
}
unread := lb.Unread(ctx, to)
if err := claude.Ring(s, doorbell(unread, me), prio); err != nil {
	fmt.Fprintf(os.Stderr, "couldn't ring %s (the message is stored): %v\n", to, err) // still exit 0
}
```

The doorbell text explains itself, so bees need no setup prompt in v0:

```text
[bs] 2 unread (queen ×2) — run: bs inbox   (reply with: bs send queen "...")
[bs] 2 unread (bees-1-3, bees-1-2) — run: bs inbox   (reply with: bs send bees-1-2 "...")
```

Senders are listed in order of their oldest unread message; "reply with" names whoever just sent.

`bs status` combines `claude.List()` with `lb.UnreadCounts()`. It shows the sessions this project's logbook names (writers, recipients, bees) that are running or have unread messages, with their unread counts. A stopped session with nothing unread is history and is left out. Claude's registry is machine-wide, so a running session this project's logbook has never named (another project's, or one that hasn't used bs yet) is left out too (R30). It writes nothing.

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
bs spawn bees-2.1       # inside the queen: bees-2-1 in bees-WT/bees-2-1
```

Manual bee (no herdr): `claude --name bee-a --settings '{"crossSessionInbound":"accept"}'` in its worktree.

## Tests

| Test | Checks |
|---|---|
| `logbook` tests | each test opens a fresh logbook file in `t.TempDir()` |
| `TestSendUnreadMarkRead` | round trip |
| `TestConcurrentSends` | 20 separate processes or connections sending at once; no loss, no busy errors |
| `TestUnreadIsSet` | reading a newer message leaves older ones unread |
| `TestRecordReceiptsIdempotent` | recording the same receipt twice is a no-op |
| `project` tests | resolution order; a linked worktree resolves to its main repo's project |
| `bee` tests (fake Herdr and Beads) | spawn → settings + entry + brief + start args; second spawn `ErrRunning`; dead agent resumes in the same workspace with `--continue` only if it read its inbox since the spawn; `agent_not_ready` counts as started; herdr `timeout` counts as started only with a live registry session; an unreadable registry starts nothing; an ended spawn's brief is retired; orphan `ErrOrphan` with the cleanup command and nothing removed; end removes exactly the recorded id; wrong repo label refused |
| `TestCheckAgentArgsMatchesRealHerdr` | the probed table of agent arguments against the installed herdr (unknown pane, so nothing starts); skipped without a running herdr. Every constraint fakeHerdr enforces comes from `CheckAgentArgs`, so this holds the fake to the real tool |
| `claude` tests (`claudetest.FakeRegistry`) | a fake unix listener sees the auth line only when a key exists, then the user line with its priority, then EOF; a `procStart` mismatch or another `peerProtocol` gives `ErrNotRunning` |
| `cmd/bs` status tests | running with 0 and N unread, not running with N unread, stopped with none left out; `--json`; status records no receipts |
| `cmd/bs` ring tests | doorbell text and `--now`; not running → stored, "queued", exit 0; failed ring → warning, exit 0 |
| `TestKilledWriterLosesNoCommittedMessage` | "Done when" 4: a separate process SIGKILLed mid-way through sends; every acknowledged send survives and the logbook still writes |
| `scripts/e2e.sh <bead>` | "Done when" 1–3 with a real queen and bee in herdr: wake time from the bee's transcript, round trip, mail queued while stopped |

## v1 outline

- **Reconciler** ([ADR-0003](adr/0003-reconcile-on-triggers-not-a-daemon.md)):
  - `Plan(Facts, now) []Action`, run from send, hooks (5 s debounce), status, inbox and a 60 s watchdog;
  - one reconcile at a time, serialized by SQLite's write lock;
  - one doorbell in flight per session, re-ring at 60 s, flag the queen at 120 s.
- **Hook:** `bs hook` on UserPromptSubmit writes a `rang`/landed entry for `[bs]` prompts; hooks and herdr agent status are the attention facts for `Plan`, never screen scraping (R38).
- **Bee contract:** a fuller system prompt for spawned bees (R11).
- **Questions and answers** (R35, R36): message `data` gains `bead` and `needs` (`none` | `answer` | `approval`); `bs send --needs … --bead … --re <id>`; an answer is a message whose `ref` is the question, and the first one wins; `bs status` counts open questions.
- **Captain** (R13, R37): a message to `captain` with `needs` set is something that needs the human; `bs status` lists them first. Desk only, no push (S3).
- **Harness seam:** `Harness{Launch, Ring, Alive}`, with an omp adapter in v1.1.
