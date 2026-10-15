# pns spec

`pns` lets Claude Code sessions in herdr panes send each other messages through a shared logbook, and wakes an idle session when it has mail. Vocabulary is in [GLOSSARY.md](../GLOSSARY.md); the reasons behind the shape are in [docs/adr/](adr/).

This spec covers **v0**, the walking skeleton. [v1](#v1-outline) is outlined at the end and gets its own spec pass.

## Scope

v0 proves the core loop: the chief sends a message, an idle peon wakes, reads it and replies, and the chief wakes. Peons are started by hand.

| In v0 | Deferred to v1 |
|---|---|
| `pns init`, `send`, `inbox`, `status` | `pns spawn`, `pns end`, `pns hook` |
| `entries` table; writes `message` and `received` | `rang` entries |
| Claude adapter: find, ring | `Launch`, herdr panes, worktrees |
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
cmd/pns/main.go            subcommands, flags, output
internal/logbook/          the entries table
internal/harness/claude/   registry lookup, auth token, NDJSON ring
internal/whoami/           which session is running pns
```

The existing `cmd/peons` is renamed to `cmd/pns`.

## Logbook

The logbook is a SQLite file in WAL mode ([ADR-0005](adr/0005-sqlite-not-dolt.md)), opened through `modernc.org/sqlite`.

```text
PNS_LOGBOOK  default  ~/.local/share/peons/logbook.db
pragmas      journal_mode=WAL, busy_timeout=5000, foreign_keys=ON; writes use BEGIN IMMEDIATE
```

```sql
CREATE TABLE IF NOT EXISTS entries (
  id      TEXT PRIMARY KEY,           -- ULID; receipts: 'rcv:<msg id>:<session>'
  ts      TEXT NOT NULL,              -- RFC 3339, UTC, milliseconds
  kind    TEXT NOT NULL CHECK (kind IN ('message','received','rang')),
  actor   TEXT NOT NULL,              -- session that wrote the entry
  subject TEXT,                       -- message: recipient
  ref     TEXT REFERENCES entries(id), -- received: the message
  data    TEXT CHECK (data IS NULL OR json_valid(data)) -- message: {"body": "..."}
) STRICT;
CREATE INDEX IF NOT EXISTS k_subj ON entries (kind, subject, id);
CREATE INDEX IF NOT EXISTS k_ref  ON entries (kind, ref);
```

Rules:

- Entries are only inserted, never updated or deleted.
- Each write is one `BEGIN IMMEDIATE` transaction; concurrent writers wait on the busy timeout instead of failing.
- A receipt id is deterministic and written with `INSERT OR IGNORE`, so a duplicate is a no-op.
- Unread is always this query. Never use a cursor or a time window.

```sql
SELECT m.id, m.actor, m.ts, m.data FROM entries m
WHERE m.kind = 'message' AND m.subject = ?
  AND NOT EXISTS (SELECT 1 FROM entries r
                  WHERE r.kind = 'received' AND r.ref = m.id AND r.actor = ?)
ORDER BY m.id;
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

func Open(ctx context.Context, path string) (*Logbook, error) // creates the file and parent dir if missing
func (l *Logbook) Init(ctx context.Context) error                 // CREATE TABLE / INDEX IF NOT EXISTS
func (l *Logbook) Send(ctx context.Context, from, to, body string) (ID, error)
func (l *Logbook) Unread(ctx context.Context, session string) ([]Message, error)
func (l *Logbook) MarkRead(ctx context.Context, session string, ids []ID) error // one commit
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

## Who am I

```go
package whoami

func Me(flagAs string) (string, error)
```

Resolved in this order:

1. The `--as` flag.
2. The `PNS_SESSION` environment variable.
3. Walk `getppid()` upward to the first pid that has `~/.claude/sessions/<pid>.json`, and use its `name`.
4. Otherwise fail with `run pns from inside a named Claude session, or pass --as`.

## Commands

```text
pns init                       create the logbook file and table if missing
pns send <to> [--now] <body|-> store a message, then ring <to>
pns inbox [--peek]             print unread; without --peek, mark them read in one commit
pns status [--json]            each known session: running or not, unread count
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

## Manual setup (v0)

```sh
pns init
# in each peon worktree, once:
mkdir -p .claude && echo '{"crossSessionInbound":"accept"}' > .claude/settings.local.json
claude --name peon-a      # names: [a-z0-9-], no ':'
# chief:
claude --name chief
```

## Tests

| Test | Checks |
|---|---|
| `logbook` tests | each test opens a fresh logbook file in `t.TempDir()` |
| `TestSendUnreadMarkRead` | round trip |
| `TestConcurrentSends` | 20 separate processes or connections sending at once; no loss, no busy errors |
| `TestUnreadIsSet` | a message with an older id committed late still shows as unread |
| `TestMarkReadIdempotent` | marking the same id twice is a no-op |
| `claude` `TestRingNDJSON` | fake unix listener sees the auth line, the user line and a half-close |
| `TestFindRejectsStalePid` | a `procStart` mismatch gives `ErrNotRunning` |
| `scripts/e2e.sh` | two real Claude sessions; checks "Done when" 1–3 |

## v1 outline

- **Reconciler** ([ADR-0003](adr/0003-reconcile-on-triggers-not-a-daemon.md)):
  - `Plan(Facts, now) []Action`, run from send, hooks (5 s debounce), status, inbox and a 60 s watchdog;
  - one reconcile at a time, serialized by SQLite's write lock;
  - one doorbell in flight per session, re-ring at 60 s, flag the chief at 120 s.
- **Hook:** `pns hook` on UserPromptSubmit writes a `rang`/landed entry for `[pns]` prompts.
- **Spawn:** `pns spawn <bead>` creates the worktree, settings, herdr pane and `claude --name peon-<slug>` with the peon contract, then rings once as a smoke test. `pns end` closes it.
- **Captain:** a message to `captain` is something that needs the human; `pns status` lists them first.
- **Harness seam:** `Harness{Launch, Ring, Alive}`, with an omp adapter in v1.1.
