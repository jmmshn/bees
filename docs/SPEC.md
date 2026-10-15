# pns spec

`pns` lets Claude Code sessions in herdr panes send each other messages through a shared logbook, and wakes an idle session when it has mail. Vocabulary is in [GLOSSARY.md](../GLOSSARY.md); the reasons behind the shape are in [docs/adr/](adr/); every decision is indexed in [DECISIONS.md](DECISIONS.md).

This spec covers **v0**, the walking skeleton, plus **spawn**, which v0 grows into for dogfooding. [v1](#v1-outline) is outlined at the end and gets its own spec pass.

## Scope

v0 proves the core loop: the chief sends a message, an idle peon wakes, reads it and replies, and the chief wakes. Spawn then lets the chief start a peon per bead in its own herdr workspace ([ADR-0006](adr/0006-spawn-and-naming.md)).

| In v0 + spawn | Deferred to v1 |
|---|---|
| `pns init`, `send`, `inbox`, `status`, `chief`, `spawn`, `end` | `pns hook` |
| `entries` table; writes `message`, `received`, `spawned`, `ended` | `rang` entries |
| Claude adapter: find, ring; herdr and bd adapters | reconciler |
| Ring every unread message on every send | `Plan` reconciler, coalescing, re-ring, flag the chief, watchdog |
| `--now` | Captain inbox, disk warning, omp adapter |

Platform: Linux only.

## Done when

1. An idle peon wakes within 2 s of `pns send`.
2. A chief → peon → chief round trip completes with nobody typing in either session.
3. Messages sent while a peon is not running are delivered on the first send after it starts.
4. A pns process killed mid-write loses no committed message and leaves the logbook readable.

## Layout

```text
cmd/pns/                   subcommands, flags, output
internal/logbook/          the entries table
internal/project/          which project, logbook and repo; which session am I
internal/peon/             Fleet.Spawn / Fleet.End; Herdr and Beads ports with CLI adapters
internal/harness/claude/   registry lookup, auth token, NDJSON ring
```

## Logbook

The logbook is a SQLite file in WAL mode ([ADR-0005](adr/0005-sqlite-not-dolt.md)), opened through `modernc.org/sqlite`.

```text
PNS_LOGBOOK  default  ~/.local/share/peons/<project>.db   (one logbook per project, R23)
pragmas      journal_mode=WAL, busy_timeout=5000, foreign_keys=ON; writes use BEGIN IMMEDIATE
```

```sql
CREATE TABLE IF NOT EXISTS entries (
  id      TEXT PRIMARY KEY,           -- time-prefixed random id; receipts: 'rcv:<msg id>:<session>'
  ts      TEXT NOT NULL,              -- RFC 3339, UTC, milliseconds
  kind    TEXT NOT NULL CHECK (kind IN ('message','received','rang','spawned','ended')),
  actor   TEXT NOT NULL,              -- session that wrote the entry
  subject TEXT,                       -- message: recipient; spawned/ended: peon name
  ref     TEXT REFERENCES entries(id), -- received: the message; ended: the spawned entry
  data    TEXT CHECK (data IS NULL OR json_valid(data)) -- message: {"body": "..."}
) STRICT;
CREATE INDEX IF NOT EXISTS k_subj ON entries (kind, subject, id);
CREATE INDEX IF NOT EXISTS k_ref  ON entries (kind, ref);
```

Rules:

- Entries are only inserted, never updated or deleted.
- Each write is one `BEGIN IMMEDIATE` transaction; concurrent writers wait on the busy timeout instead of failing.
- A receipt id is deterministic and written with `INSERT OR IGNORE`, so a duplicate is a no-op.
- `Open` creates the table and indexes if missing, so `pns init` is optional.
- Unread is always this query, in insertion order (`rowid`). Never use a cursor or a time window.

```sql
SELECT m.id, m.actor, m.ts, m.data FROM entries m
WHERE m.kind = 'message' AND m.subject = ?
  AND NOT EXISTS (SELECT 1 FROM entries r
                  WHERE r.kind = 'received' AND r.ref = m.id AND r.actor = ?)
ORDER BY m.rowid;
```

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
func (l *Logbook) Counts(ctx context.Context) (map[string]int, error)          // unread per recipient
```

## Claude adapter

```go
package claude

type Priority string // "now" | "next"

type Session struct {
	Name      string
	PID       int
	ProcStart int64
	Socket    string
	Token     string // empty when the session has no key file
}

func List() ([]Session, error)        // every live, compatible session
func Find(name string) (Session, error) // ErrNotRunning when absent or stale
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
{"type":"user","message":{"role":"user","content":"<text>"},"priority":"next","msg_id":"<uuid>"}
```

A receiving session must have `"crossSessionInbound": "accept"` in its settings. Otherwise a session in bypass mode holds peer messages instead of acting on them.

## Project and identity

```go
package project

type Project struct{ Name, Repo, Logbook string }
func Resolve(flag string) (Project, error) // --project → PNS_PROJECT → basename of git's common dir
func (p Project) Chief() string             // "chief-" + Name
func Me(flagAs string) (string, error)      // --as → PNS_SESSION → parent-process walk
```

- The project comes from git's *common* directory, so a shell inside `<repo>-WT/peon-995-8` resolves to the main repo's project.
- `Repo` is `$PNS_REPOS/<project>` (default `~/repos/jmmshn`); `Logbook` is `$PNS_LOGBOOK`, else `~/.local/share/peons/<project>.db`.
- `Me` walks `getppid()` upward to the first pid with `~/.claude/sessions/<pid>.json` and uses its `name`; otherwise it fails with `run pns from inside a named Claude session, or pass --as`.

## Spawn

```go
package peon

func Name(bead string) (string, error) // beads-995.8 → peon-995-8; refuses names herdr can't take

type Herdr interface {
	CreateWorktree(ctx context.Context, repo, branch, path, label string) (Workspace, error)
	StartClaude(ctx context.Context, pane, name string, args ...string) error
	RemoveWorktree(ctx context.Context, workspace string) error // already gone → nil
	Agent(ctx context.Context, name string) (Agent, error)      // ErrNoAgent when not live
	Workspaces(ctx context.Context) ([]Workspace, error)
}
type Beads interface{ Show(ctx context.Context, id string) (Bead, error) }

type Fleet struct {
	P  project.Project
	LB *logbook.Logbook
	H  Herdr
	B  Beads
	Me string
}
func (f Fleet) Spawn(ctx context.Context, bead string) (string, error) // ErrRunning, ErrWrongRepo, ErrOrphan
func (f Fleet) End(ctx context.Context, name string) error              // ErrNotSpawned; already ended → nil
```

`Spawn(bead)`:

1. `name = Name(bead)`.
2. If `LatestSpawn(name)` is open:
   - herdr reports the agent live → `ErrRunning`;
   - otherwise → resume at step 8 in the recorded workspace.
3. The bead's repo label (its own, else its nearest ancestor's) must equal the project, unless `--repo` → else `ErrWrongRepo`.
4. A herdr workspace labelled with the bead but no `spawned` entry → `ErrOrphan`. It is never removed automatically.
5. `herdr worktree create --cwd <repo> --branch <name> --path <repo>-WT/<name> --label <bead> --no-focus --json`.
6. `RecordSpawn` immediately: subject = name; data = bead, workspace, pane, worktree, branch.
7. Send the brief (`bd show` title and description) as a message from the chief.
8. `herdr agent start <name> --kind claude --pane <pane> -- --name <name> --settings '{"crossSessionInbound":"accept"}' "<doorbell>"`. The doorbell is Claude's first prompt, so spawn doesn't depend on ringing. No settings file is written (Claude reads `settings.local.json` from the main checkout in a worktree), and no `PNS_*` variables are needed.

`End(name)`: read `LatestSpawn`; none → `ErrNotSpawned` (never ask herdr). Otherwise `herdr worktree remove --workspace <recorded id> --force`, then `git branch -d <name>` (kept if not fully merged), then `RecordEnd`.

## Commands

```text
pns init                       create the logbook file and table if missing (optional)
pns chief                      start the chief: claude --name chief-<project> in the repo
pns spawn <bead> [--repo P]    start a peon for the bead in its own herdr workspace
pns end <peon>                 remove the peon's recorded workspace
pns send <to> [--now] <body|-> store a message, then ring <to>   (flags may follow <to>)
pns inbox [--peek]             print unread; without --peek, record receipts in one commit
pns status [--json]            each known session: running or not, unread count
every command                  --project, --as
```

`pns send`:

```go
id := lb.Send(ctx, me, to, body)
unread := lb.Unread(ctx, to)
s, err := claude.Find(to)
if errors.Is(err, claude.ErrNotRunning) {
	fmt.Printf("queued %s; %s is not running\n", id, to)
	return nil
}
if err := claude.Ring(s, doorbell(unread), prio); err != nil {
	fmt.Fprintf(os.Stderr, "queued %s; ring failed: %v\n", id, err) // still exit 0: the message is stored
}
```

The doorbell text explains itself, so peons need no setup prompt in v0:

```text
[pns] 2 unread (chief ×2) — run: pns inbox   (reply with: pns send chief "...")
```

`pns status` combines `claude.List()` with `lb.Counts()`. It shows sessions that are running plus any recipient that has unread messages but is not running.

## Setup

```sh
pns chief                 # chief-<project>, in $PNS_REPOS/<project>
pns spawn beads-995.8     # inside the chief: peon-995-8 in peons-WT/peon-995-8
```

Manual peon (no herdr): `claude --name peon-a --settings '{"crossSessionInbound":"accept"}'` in its worktree.

## Tests

| Test | Checks |
|---|---|
| `logbook` tests | each test opens a fresh logbook file in `t.TempDir()` |
| `TestSendUnreadMarkRead` | round trip |
| `TestConcurrentSends` | 20 separate processes or connections sending at once; no loss, no busy errors |
| `TestUnreadIsSet` | reading a newer message leaves older ones unread |
| `TestRecordReceiptsIdempotent` | recording the same receipt twice is a no-op |
| `project` tests | resolution order; a linked worktree resolves to its main repo's project |
| `peon` tests (fake Herdr and Beads) | spawn → settings + entry + brief + start args; second spawn `ErrRunning`; dead agent resumes in the same workspace; orphan `ErrOrphan` and nothing removed; end removes exactly the recorded id; wrong repo label refused |
| `claude` `TestRingNDJSON` | fake unix listener sees the auth line, the user line and a half-close |
| `TestFindRejectsStalePid` | a `procStart` mismatch gives `ErrNotRunning` |
| `scripts/e2e.sh` | two real Claude sessions; checks "Done when" 1–3 |

## v1 outline

- **Reconciler** ([ADR-0003](adr/0003-reconcile-on-triggers-not-a-daemon.md)):
  - `Plan(Facts, now) []Action`, run from send, hooks (5 s debounce), status, inbox and a 60 s watchdog;
  - one reconcile at a time, serialized by SQLite's write lock;
  - one doorbell in flight per session, re-ring at 60 s, flag the chief at 120 s.
- **Hook:** `pns hook` on UserPromptSubmit writes a `rang`/landed entry for `[pns]` prompts.
- **Peon contract:** a fuller system prompt for spawned peons (R11).
- **Captain:** a message to `captain` is something that needs the human; `pns status` lists them first.
- **Harness seam:** `Harness{Launch, Ring, Alive}`, with an omp adapter in v1.1.
