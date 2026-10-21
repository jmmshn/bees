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
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jmmshn/bees/internal/harness"
)

// ID identifies an entry. Message ids sort by creation time.
type ID string

// Message is one entry addressed from one session to another.
type Message struct {
	ID       ID
	From, To string
	Body     string
	TS       time.Time
}

// Logbook is an open logbook file.
type Logbook struct{ db *sql.DB }

// table is the DDL of the entries table of schema version 1, named name (ADR-0009):
// version 0 lacked the queen, started and done kinds. started and done have no writers
// yet (bees-12.10's next stage, bees-12.18): one rebuild widens the CHECK for all three.
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
	q.Add("_pragma", "busy_timeout(5000)") // first, so the WAL switch below waits instead of failing
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, db, abs); err != nil {
		db.Close()
		return nil, fmt.Errorf("open logbook %s: %w", path, err)
	}
	return &Logbook{db: db}, nil
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
	now := time.Now().UTC()
	id := newID(now)
	data, err := json.Marshal(map[string]string{"body": body})
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

// Unread returns the messages addressed to session that have no receipt from it, in the
// order they were stored, leaving out the briefs of ended spawns.
func (l *Logbook) Unread(ctx context.Context, session string) ([]Message, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT m.id, m.ts, m.actor, m.subject, json_extract(m.data, '$.body') FROM entries m
WHERE m.kind = 'message' AND m.subject = ?1
  AND NOT EXISTS (SELECT 1 FROM entries r
                  WHERE r.kind = 'received' AND r.ref = m.id AND r.actor = ?1)
  AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = m.ref)
ORDER BY m.rowid`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var ts string
		if err := rows.Scan(&m.ID, &ts, &m.From, &m.To, &m.Body); err != nil {
			return nil, err
		}
		if m.TS, err = time.Parse(tsLayout, ts); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
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
            FROM entries m WHERE m.kind = 'message'
              AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = m.ref)
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
	Repo      string `json:"repo"`           // where the branch lives, for bs end
	Base      string `json:"base,omitempty"` // the checkout the branch was cut from (Repo, or a linked worktree); empty before bees-11.3
	Ended     bool   `json:"-"`
	Read      bool   `json:"-"` // the worker has read a message since this spawn, so it has a conversation
	// Harness is the config name of the harness the worker started on; an entry from
	// before bees-12.6 reads as harness.Default. Record is that harness's Launch.Record.
	Harness string            `json:"harness,omitempty"`
	Record  map[string]string `json:"record,omitempty"`
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
