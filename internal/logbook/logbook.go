// Package logbook is the append-only record of every message in bees (ADR-0001, ADR-0002),
// stored in a local SQLite file (ADR-0005).
package logbook

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/proc"
)

// ID identifies an entry. Message ids sort by creation time.
type ID string

// Message is one entry addressed from one session to another.
type Message struct {
	ID          ID
	From, To    string
	FromProject string // the sender's project when it wrote from another one's logbook; "" otherwise
	Body        string
	TS          time.Time
}

// Logbook is an open logbook file.
type Logbook struct{ db *sql.DB }

// table is the DDL of the entries table of schema version 1, named name (ADR-0009):
// version 0 lacked the queen, started and done kinds; one rebuild widened the CHECK for
// all three.
func table(name string) string {
	return `CREATE TABLE IF NOT EXISTS ` + name + ` (
  id      TEXT PRIMARY KEY,
  ts      TEXT NOT NULL,
  kind    TEXT NOT NULL CHECK (kind IN ('message','received','rang','spawned','ended','queen','started','done')),
  actor   TEXT NOT NULL,
  subject TEXT,
  ref     TEXT REFERENCES ` + name + `(id),
  data    TEXT CHECK (data IS NULL OR json_valid(data))
) STRICT`
}

const indexes = `
CREATE INDEX IF NOT EXISTS k_subj ON entries (kind, subject, id);
CREATE INDEX IF NOT EXISTS k_ref  ON entries (kind, ref);
`

// version is the schema's PRAGMA user_version.
const version = 1

// Open opens the logbook at path, creating the file, its directory and the schema if
// missing, and migrating an older schema (migrate).
// Writes take SQLite's write lock up front (BEGIN IMMEDIATE) and wait on a busy timeout.
func Open(ctx context.Context, path string) (*Logbook, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil { // other local users must not read the mail
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)") // writes wait; a new file's WAL switch can still fail: see below
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Two first opens of a new file race on the WAL switch, which SQLite can refuse with
	// SQLITE_BUSY without waiting on busy_timeout (bees-12.23): retry, for a bounded time.
	deadline := time.Now().Add(openWait)
	for err = migrate(ctx, db, abs); busy(err) && time.Now().Before(deadline); err = migrate(ctx, db, abs) {
		select {
		case <-ctx.Done():
			err = errors.Join(err, ctx.Err())
		case <-time.After(time.Duration(10+mrand.IntN(40)) * time.Millisecond): // jitter: contenders don't retry in step
			continue
		}
		break
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open logbook %s: %w", path, err)
	}
	return &Logbook{db: db}, nil
}

// openWait bounds when Open starts a retry on SQLITE_BUSY; one attempt may also wait out
// busy_timeout, so Open gives up within about openWait plus 5 s.
const openWait = 5 * time.Second

// busy reports whether err is SQLite's SQLITE_BUSY, extended codes included.
func busy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

// migrate brings the schema to version. A new file gets the table as it is now. A
// version 0 file is copied aside to <path>.pre-1 first (motif 14), then its table is
// rebuilt once with the wider kind CHECK, which SQLite can't alter in place: inside
// BEGIN IMMEDIATE, the write lock every writer takes, so a concurrent Open waits and then
// finds the work done. An older bs keeps working on the rebuilt table: its reads select
// by kind and its writes satisfy the wider CHECK.
func migrate(ctx context.Context, db *sql.DB, path string) error {
	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= version {
		return nil
	}
	old, err := hasEntries(ctx, db)
	if err != nil {
		return err
	}
	if old {
		if err := copyAside(ctx, db, fmt.Sprintf("%s.pre-%d", path, version)); err != nil {
			return fmt.Errorf("copy the logbook aside before migrating it: %w", err)
		}
	}
	// One connection: foreign_keys is per connection and can't change inside a transaction,
	// and the rebuild drops the table its own rows' ref points into.
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	err = rebuild(ctx, c)
	// The connection goes back to the pool: a failed reset fails Open, which closes it.
	_, onErr := c.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`)
	return errors.Join(err, onErr)
}

// hasEntries reports whether db has an entries table.
func hasEntries(ctx context.Context, db querier) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'entries'`).Scan(&n)
	return n > 0, err
}

// rebuild makes the version 1 table on c, unless another Open did while c waited.
func rebuild(ctx context.Context, c *sql.Conn) error {
	tx, err := c.BeginTx(ctx, nil) // BEGIN IMMEDIATE (_txlock)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= version {
		return nil // another Open migrated while this one waited for the lock
	}
	old, err := hasEntries(ctx, tx)
	if err != nil {
		return err
	}
	stmts := []string{table("entries")}
	if old {
		stmts = []string{
			table("entries_v1"),
			`INSERT INTO entries_v1 (id, ts, kind, actor, subject, ref, data) SELECT id, ts, kind, actor, subject, ref, data FROM entries ORDER BY rowid`,
			`DROP TABLE entries`,
			`ALTER TABLE entries_v1 RENAME TO entries`,
		}
	}
	stmts = append(stmts, indexes, fmt.Sprintf(`PRAGMA user_version = %d`, version))
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// copyAside writes a consistent copy of db to path, keeping an earlier copy there: a
// concurrent Open may have made one already.
func copyAside(ctx context.Context, db *sql.DB, path string) error {
	tmp := fmt.Sprintf("%s.%s.tmp", path, newID(time.Now()))
	defer os.Remove(tmp) // a failed VACUUM may leave part of it
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	switch err := os.Link(tmp, path); {
	case err == nil, errors.Is(err, os.ErrExist):
		return nil
	case errors.Is(err, os.ErrPermission), errors.Is(err, errors.ErrUnsupported):
		if _, err := os.Lstat(path); err == nil { // a file system without hard links
			return nil
		}
		return os.Rename(tmp, path)
	default:
		return err
	}
}

// Close closes the logbook.
func (l *Logbook) Close() error { return l.db.Close() }

// Send stores a message from one session to another and returns its id.
func (l *Logbook) Send(ctx context.Context, from, to, body string) (ID, error) {
	return l.SendFrom(ctx, from, "", to, body)
}

// SendFrom is Send from a session of project fromProject, writing into another project's
// logbook (ADR-0009 amendment 1): the entry records it, so the reply needs no project read
// back out of a name (motif 3). "" is the logbook's own project.
func (l *Logbook) SendFrom(ctx context.Context, from, fromProject, to, body string) (ID, error) {
	now := time.Now().UTC()
	id := newID(now)
	d := map[string]string{"body": body}
	if fromProject != "" {
		d["from_project"] = fromProject
	}
	data, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO entries (id, ts, kind, actor, subject, data) VALUES (?, ?, 'message', ?, ?, ?)`,
		string(id), now.Format(tsLayout), from, to, string(data)); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

// Unread returns the messages and done entries addressed to session that have no receipt
// from it, in the order they were stored, leaving out the briefs of ended spawns. A done
// stays unread after its worker has ended (ADR-0009 amendment 1); its Body is Done.Text.
func (l *Logbook) Unread(ctx context.Context, session string) ([]Message, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT m.id, m.ts, m.actor, m.subject, coalesce(json_extract(m.data, '$.from_project'), ''), m.kind, m.data FROM entries m
WHERE m.kind IN ('message', 'done') AND m.subject = ?1
  AND NOT EXISTS (SELECT 1 FROM entries r
                  WHERE r.kind = 'received' AND r.ref = m.id AND r.actor = ?1)
  AND (m.kind = 'done' OR NOT EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = m.ref))
ORDER BY m.rowid`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var ts, kind, data string
		if err := rows.Scan(&m.ID, &ts, &m.From, &m.To, &m.FromProject, &kind, &data); err != nil {
			return nil, err
		}
		if err := m.decode(kind, data); err != nil {
			return nil, err
		}
		if m.TS, err = time.Parse(tsLayout, ts); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// decode fills m's Body from a message or done entry's data.
func (m *Message) decode(kind, data string) error {
	if kind == "done" {
		var d Done
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			return fmt.Errorf("done entry %s: %w", m.ID, err)
		}
		m.Body = d.Text()
		return nil
	}
	var d struct{ Body string }
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return fmt.Errorf("message %s: %w", m.ID, err)
	}
	m.Body = d.Body
	return nil
}

// UnreadCounts returns every session the logbook names (a writer, recipient or worker) with its
// unread count, which is 0 for sessions with nothing unread.
func (l *Logbook) UnreadCounts(ctx context.Context) (map[string]int, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT name, SUM(unread) FROM (
  SELECT actor AS name, 0 AS unread FROM entries
  UNION ALL SELECT subject, 0 FROM entries WHERE kind = 'spawned'
  UNION ALL SELECT m.subject, NOT EXISTS (SELECT 1 FROM entries r
                                         WHERE r.kind = 'received' AND r.ref = m.id AND r.actor = m.subject)
            FROM entries m WHERE m.kind IN ('message', 'done')
              AND (m.kind = 'done' OR NOT EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = m.ref))
) GROUP BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}

// RecordReceipts records that session was shown the given messages, in one transaction.
// A receipt's id is derived from the message and session, so repeating it is a no-op.
func (l *Logbook) RecordReceipts(ctx context.Context, session string, ids []ID) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := time.Now().UTC().Format(tsLayout)
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO entries (id, ts, kind, actor, ref) VALUES (?, ?, 'received', ?, ?)`,
			"rcv:"+string(id)+":"+session, ts, session, string(id)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// tsLayout is RFC 3339 in UTC with fixed milliseconds.
const tsLayout = "2006-01-02T15:04:05.000Z07:00"

// newID returns a unique 26-character id: 13 hex digits of Unix milliseconds,
// then 13 hex digits of randomness. Order comes from rowid, not the id.
func newID(t time.Time) ID {
	var b [8]byte
	rand.Read(b[:])
	return ID(fmt.Sprintf("%013x%013x", t.UnixMilli(), binary.BigEndian.Uint64(b[:])>>12))
}

// Spawn records a worker bs started: its herdr workspace is the key bs end needs once
// the agent is gone (ADR-0006). It says nothing about whether the worker is running.
type Spawn struct {
	ID        ID     `json:"-"`
	Name      string `json:"-"`
	Bead      string `json:"bead"`
	Label     string `json:"label,omitempty"` // its herdr workspace label; empty before bees-1.20
	Workspace string `json:"workspace"`
	Pane      string `json:"pane"`
	Worktree  string `json:"worktree"`
	Branch    string `json:"branch"`
	Repo      string `json:"repo"`                     // where the branch lives, for bs end
	Base      string `json:"base,omitempty"`           // the checkout the branch was cut from (Repo, or a linked worktree); empty before bees-11.3
	Kind      string `json:"workspace_kind,omitempty"` // "none", or "" for git_worktree (ADR-0009)
	Dir       string `json:"dir,omitempty"`            // kind none: the directory the workspace was opened on
	Ended     bool   `json:"-"`
	Read      bool   `json:"-"` // the worker has read a message since this spawn, so it has a conversation
	// Harness is the config name of the harness the worker started on; an entry from
	// before bees-12.6 reads as harness.Default. Record is that harness's Launch.Record.
	Harness string            `json:"harness,omitempty"`
	Record  map[string]string `json:"record,omitempty"`
	Path    []string          `json:"path,omitempty"` // bs spawn --path; none before bees-8.19
}

// RecordSpawn records that by spawned s.Name and, in the same transaction, sends it
// brief (if any), so a crash can't leave a recorded worker without its brief. The brief
// refers to the spawn: once the spawn has ended, an unread brief is no longer unread.
func (l *Logbook) RecordSpawn(ctx context.Context, by string, s Spawn, brief string) (ID, error) {
	now := time.Now().UTC()
	id := newID(now)
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO entries (id, ts, kind, actor, subject, data) VALUES (?, ?, 'spawned', ?, ?, ?)`,
		string(id), now.Format(tsLayout), by, s.Name, string(data)); err != nil {
		return "", err
	}
	if brief != "" {
		body, err := json.Marshal(map[string]string{"body": brief})
		if err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO entries (id, ts, kind, actor, subject, ref, data) VALUES (?, ?, 'message', ?, ?, ?, ?)`,
			string(newID(now)), now.Format(tsLayout), by, s.Name, string(id), string(body)); err != nil {
			return "", err
		}
	}
	return id, tx.Commit()
}

// RecordEnd records that by ended the given spawn. Ending it again is a no-op.
func (l *Logbook) RecordEnd(ctx context.Context, by string, spawn ID) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO entries (id, ts, kind, actor, subject, ref)
SELECT 'end:' || id, ?, 'ended', ?, subject, id FROM entries WHERE id = ? AND kind = 'spawned'`,
		time.Now().UTC().Format(tsLayout), by, string(spawn)); err != nil {
		return err
	}
	return tx.Commit()
}

// LatestSpawn returns the most recent spawn of the named worker, whether it has ended, and
// whether the worker has read a message since. ok is false when the worker was never spawned.
func (l *Logbook) LatestSpawn(ctx context.Context, name string) (s Spawn, ok bool, err error) {
	var data string
	err = l.db.QueryRowContext(ctx, `
SELECT s.id, s.data, EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = s.id),
       EXISTS (SELECT 1 FROM entries r WHERE r.kind = 'received' AND r.actor = s.subject AND r.rowid > s.rowid)
FROM entries s WHERE s.kind = 'spawned' AND s.subject = ? ORDER BY s.rowid DESC LIMIT 1`, name,
	).Scan(&s.ID, &data, &s.Ended, &s.Read)
	if errors.Is(err, sql.ErrNoRows) {
		return Spawn{}, false, nil
	}
	if err != nil {
		return Spawn{}, false, err
	}
	s.Name = name
	if err := s.decode(data); err != nil {
		return Spawn{}, false, err
	}
	return s, true, nil
}

// decode fills s from a spawned entry's data.
func (s *Spawn) decode(data string) error {
	if err := json.Unmarshal([]byte(data), s); err != nil {
		return fmt.Errorf("spawn record %s: %w", s.ID, err)
	}
	s.Harness = cmp.Or(s.Harness, harness.Default)
	return nil
}

// EndedSpawns returns the latest spawn of each worker whose latest spawn has ended, by
// name. A worker spawned again since it ended is live, so it is left out.
func (l *Logbook) EndedSpawns(ctx context.Context) ([]Spawn, error) { return l.latestSpawns(ctx, true) }

// LiveSpawns returns the latest spawn of each worker whose latest spawn has not ended, by
// name: the workers bs end --all ends.
func (l *Logbook) LiveSpawns(ctx context.Context) ([]Spawn, error) { return l.latestSpawns(ctx, false) }

// latestSpawns returns each worker's latest spawn, by name, if its ended state is ended.
func (l *Logbook) latestSpawns(ctx context.Context, ended bool) ([]Spawn, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT s.id, s.subject, s.data FROM entries s
WHERE s.kind = 'spawned'
  AND s.rowid = (SELECT MAX(x.rowid) FROM entries x WHERE x.kind = 'spawned' AND x.subject = s.subject)
  AND EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = s.id) = ?
ORDER BY s.subject`, ended)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Spawn
	for rows.Next() {
		var s Spawn
		var data string
		if err := rows.Scan(&s.ID, &s.Name, &data); err != nil {
			return nil, err
		}
		if err := s.decode(data); err != nil {
			return nil, err
		}
		s.Ended = ended
		out = append(out, s)
	}
	return out, rows.Err()
}

// Queen is a queen entry, written by bs queen before it execs the harness (ADR-0009):
// the herdr pane she runs in and her process, whose pid and start time exec keeps. A queen entry is never closed: the newest is
// the queen.
type Queen struct {
	ID      ID     `json:"-"`
	Name    string `json:"-"`
	Harness string `json:"harness"`
	Pane    string `json:"pane"`
	PID     int    `json:"pid"`
	Start   string `json:"start"` // the process's start time (proc.Start)
}

// RecordQueen records queen q, unless the newest queen entry of q.Name is one alive
// reports running: then it returns that entry and ok false. The check and the record share
// one write transaction, so of two queens starting at once only one is recorded (motif 7).
func (l *Logbook) RecordQueen(ctx context.Context, q Queen, alive func(Queen) (bool, error)) (running Queen, ok bool, err error) {
	now := time.Now().UTC()
	data, err := json.Marshal(q)
	if err != nil {
		return Queen{}, false, err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return Queen{}, false, err
	}
	defer tx.Rollback()
	prev, found, err := latestQueen(ctx, tx, q.Name)
	if err != nil {
		return Queen{}, false, err
	}
	if found {
		switch up, err := alive(prev); {
		case err != nil:
			return Queen{}, false, err
		case up:
			return prev, false, nil
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO entries (id, ts, kind, actor, subject, data) VALUES (?, ?, 'queen', ?, ?, ?)`,
		string(newID(now)), now.Format(tsLayout), q.Name, q.Name, string(data)); err != nil {
		return Queen{}, false, err
	}
	return Queen{}, true, tx.Commit()
}

// LatestQueen returns the newest queen entry for name; ok is false when there is none.
func (l *Logbook) LatestQueen(ctx context.Context, name string) (Queen, bool, error) {
	return latestQueen(ctx, l.db, name)
}

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func latestQueen(ctx context.Context, db querier, name string) (q Queen, ok bool, err error) {
	var data string
	err = db.QueryRowContext(ctx, `SELECT id, data FROM entries WHERE kind = 'queen' AND subject = ? ORDER BY rowid DESC LIMIT 1`, name).Scan(&q.ID, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return Queen{}, false, nil
	}
	if err != nil {
		return Queen{}, false, err
	}
	q.Name = name
	if err := json.Unmarshal([]byte(data), &q); err != nil {
		return Queen{}, false, fmt.Errorf("queen record %s: %w", q.ID, err)
	}
	return q, true, nil
}

// Start records the harness process a confirmed start of a worker found in its pane
// (ADR-0009 amendment 1): the one fact behind its liveness. Baseline is the process's
// descendants when it was first seen, for the background-work gate (bees-12.18);
// BaselineErr says why it couldn't be read, so the gate fails closed.
type Start struct {
	ID    ID     `json:"-"`
	Spawn ID     `json:"-"`
	Name  string `json:"-"`
	proc.Process
	Harness     string         `json:"harness"`
	Exe         string         `json:"exe,omitempty"`
	Baseline    []proc.Process `json:"baseline"`
	BaselineErr string         `json:"baseline_error,omitempty"`
}

// RecordStart records start s of the worker whose open spawned entry is s.Spawn.
func (l *Logbook) RecordStart(ctx context.Context, s Start) (ID, error) {
	now := time.Now().UTC()
	id := newID(now)
	if s.Baseline == nil {
		s.Baseline = []proc.Process{}
	}
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	_, err = l.db.ExecContext(ctx, `INSERT INTO entries (id, ts, kind, actor, subject, ref, data) VALUES (?, ?, 'started', ?, ?, ?, ?)`,
		string(id), now.Format(tsLayout), s.Name, s.Name, string(s.Spawn), string(data))
	return id, err
}

// LatestStart returns the newest start recorded for spawn; ok is false when the spawn has
// none (never confirmed, or spawned before bs recorded starts).
func (l *Logbook) LatestStart(ctx context.Context, spawn ID) (s Start, ok bool, err error) {
	var data string
	err = l.db.QueryRowContext(ctx, `SELECT id, subject, data FROM entries WHERE kind = 'started' AND ref = ? ORDER BY rowid DESC LIMIT 1`, string(spawn)).Scan(&s.ID, &s.Name, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return Start{}, false, nil
	}
	if err != nil {
		return Start{}, false, err
	}
	s.Spawn = spawn
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		return Start{}, false, fmt.Errorf("start record %s: %w", s.ID, err)
	}
	return s, true, nil
}

// OpenReadOnly opens the logbook at path for reading only: it makes no logbook,
// directory, schema or migration (SQLite may add its -shm and -wal beside it), so scanning
// every logbook for a pane (ADR-0009 amendment 1) can't create, rebuild or split one. A
// missing file is an error.
func OpenReadOnly(ctx context.Context, path string) (*Logbook, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "query_only(1)")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String())
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open logbook %s: %w", path, err)
	}
	return &Logbook{db: db}, nil
}

// Recorded is a session whose entry recorded a herdr pane: an open spawn's worker, or a
// queen whose newest queen entry has the pane.
type Recorded struct {
	Name, Harness string
	Queen         bool
	Process       proc.Process // the process that entry or its newest start recorded
	HasProcess    bool         // a worker with no start recorded yet has none
}

// InPane returns the sessions whose entries recorded pane: open spawns (each with its
// newest start's process) and queens whose newest queen entry has it. A logbook from
// before queen and started entries just has none of them.
//
// A file with no entries table yet (one Open is still creating) records nothing.
func (l *Logbook) InPane(ctx context.Context, pane string) ([]Recorded, error) {
	if ok, err := hasEntries(ctx, l.db); err != nil || !ok {
		return nil, err
	}
	var out []Recorded
	rows, err := l.db.QueryContext(ctx, `
SELECT 0, s.subject, json_extract(s.data, '$.harness'),
       (SELECT st.data FROM entries st WHERE st.kind = 'started' AND st.ref = s.id ORDER BY st.rowid DESC LIMIT 1)
FROM entries s
WHERE s.kind = 'spawned' AND json_extract(s.data, '$.pane') = ?1
  AND s.rowid = (SELECT MAX(x.rowid) FROM entries x WHERE x.kind = 'spawned' AND x.subject = s.subject)
  AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = s.id)
UNION ALL
SELECT 1, q.subject, json_extract(q.data, '$.harness'), q.data FROM entries q
WHERE q.kind = 'queen' AND json_extract(q.data, '$.pane') = ?1
  AND q.rowid = (SELECT MAX(x.rowid) FROM entries x WHERE x.kind = 'queen' AND x.subject = q.subject)`, pane)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Recorded
		var h, data sql.NullString
		if err := rows.Scan(&r.Queen, &r.Name, &h, &data); err != nil {
			return nil, err
		}
		r.Harness = cmp.Or(h.String, harness.Default)
		if data.Valid {
			if err := json.Unmarshal([]byte(data.String), &r.Process); err != nil {
				return nil, fmt.Errorf("entry of %s: %w", r.Name, err)
			}
			r.HasProcess = r.Process.PID > 0
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Done is a done entry: worker Name reporting its bead finished to its queen (bs done,
// ADR-0009 amendment 1), with a PR or a report. A second bs done is a new entry, and the
// newest wins. Background holds what bs done --force found running.
type Done struct {
	ID         ID                `json:"-"`
	Spawn      ID                `json:"-"`
	Name       string            `json:"-"`
	Queen      string            `json:"-"`
	TS         time.Time         `json:"-"`
	Bead       string            `json:"bead"`
	PR         string            `json:"pr,omitempty"`
	Report     string            `json:"report,omitempty"`
	Background []proc.Background `json:"background,omitempty"`
}

// Text is d as its queen's inbox shows it.
func (d Done) Text() string {
	s := "done: " + d.Bead
	if d.PR != "" {
		s += " PR " + d.PR
	}
	if d.Report != "" {
		s += "\n" + d.Report
	}
	if len(d.Background) > 0 {
		s += "\n(reported with bs done --force while it still ran background work:"
		for _, b := range d.Background {
			s += "\n  " + b.String()
		}
		s += ")"
	}
	return s
}

// RecordDone records done d of the worker whose open spawned entry is d.Spawn, addressed
// to d.Queen.
func (l *Logbook) RecordDone(ctx context.Context, d Done) (ID, error) {
	now := time.Now().UTC()
	id := newID(now)
	data, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	_, err = l.db.ExecContext(ctx, `INSERT INTO entries (id, ts, kind, actor, subject, ref, data) VALUES (?, ?, 'done', ?, ?, ?, ?)`,
		string(id), now.Format(tsLayout), d.Name, d.Queen, string(d.Spawn), string(data))
	return id, err
}

// LatestDone returns the newest done recorded for spawn; ok is false when there is none.
func (l *Logbook) LatestDone(ctx context.Context, spawn ID) (d Done, ok bool, err error) {
	var data, ts string
	err = l.db.QueryRowContext(ctx, `SELECT id, ts, actor, subject, data FROM entries WHERE kind = 'done' AND ref = ? ORDER BY rowid DESC LIMIT 1`, string(spawn)).Scan(&d.ID, &ts, &d.Name, &d.Queen, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return Done{}, false, nil
	}
	if err != nil {
		return Done{}, false, err
	}
	d.Spawn = spawn
	if d.TS, err = time.Parse(tsLayout, ts); err != nil {
		return Done{}, false, err
	}
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return Done{}, false, fmt.Errorf("done entry %s: %w", d.ID, err)
	}
	return d, true, nil
}
