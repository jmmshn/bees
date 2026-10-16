// Package logbook is the append-only record of every message in bees (ADR-0001, ADR-0002),
// stored in a local SQLite file (ADR-0005).
package logbook

import (
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

const schema = `
CREATE TABLE IF NOT EXISTS entries (
  id      TEXT PRIMARY KEY,
  ts      TEXT NOT NULL,
  kind    TEXT NOT NULL CHECK (kind IN ('message','received','rang','spawned','ended')),
  actor   TEXT NOT NULL,
  subject TEXT,
  ref     TEXT REFERENCES entries(id),
  data    TEXT CHECK (data IS NULL OR json_valid(data))
) STRICT;
CREATE INDEX IF NOT EXISTS k_subj ON entries (kind, subject, id);
CREATE INDEX IF NOT EXISTS k_ref  ON entries (kind, ref);
`

// Open opens the logbook at path, creating the file, its directory and the schema if missing.
// Writes take SQLite's write lock up front (BEGIN IMMEDIATE) and wait on a busy timeout.
func Open(ctx context.Context, path string) (*Logbook, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
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
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("open logbook %s: %w", path, err)
	}
	return &Logbook{db: db}, nil
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

// UnreadCounts returns every session the logbook names (a writer, recipient or bee) with its
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

// Spawn records a bee bs started: its herdr workspace is the key bs end needs once
// the agent is gone (ADR-0006). It says nothing about whether the bee is running.
type Spawn struct {
	ID        ID     `json:"-"`
	Name      string `json:"-"`
	Bead      string `json:"bead"`
	Label     string `json:"label,omitempty"` // its herdr workspace label; empty before bees-1.20
	Workspace string `json:"workspace"`
	Pane      string `json:"pane"`
	Worktree  string `json:"worktree"`
	Branch    string `json:"branch"`
	Repo      string `json:"repo"` // where the branch lives, for bs end
	Ended     bool   `json:"-"`
	Read      bool   `json:"-"` // the bee has read a message since this spawn, so it has a conversation
}

// RecordSpawn records that by spawned s.Name and, in the same transaction, sends it
// brief (if any), so a crash can't leave a recorded bee without its brief. The brief
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

// LatestSpawn returns the most recent spawn of the named bee, whether it has ended, and
// whether the bee has read a message since. ok is false when the bee was never spawned.
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
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		return Spawn{}, false, fmt.Errorf("spawn record %s: %w", s.ID, err)
	}
	return s, true, nil
}

// EndedSpawns returns the latest spawn of each bee whose latest spawn has ended, by
// name. A bee spawned again since it ended is live, so it is left out.
func (l *Logbook) EndedSpawns(ctx context.Context) ([]Spawn, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT s.id, s.subject, s.data FROM entries s
WHERE s.kind = 'spawned'
  AND s.rowid = (SELECT MAX(x.rowid) FROM entries x WHERE x.kind = 'spawned' AND x.subject = s.subject)
  AND EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = s.id)
ORDER BY s.subject`)
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
		if err := json.Unmarshal([]byte(data), &s); err != nil {
			return nil, fmt.Errorf("spawn record %s: %w", s.ID, err)
		}
		s.Ended = true
		out = append(out, s)
	}
	return out, rows.Err()
}

// RecordExit tells to, from "bs", that the bee name's Claude process proc (its /proc
// start time) exited under the given spawn, unless the spawn has ended or been
// superseded, or the bee said "done: <bead>" since it. The fence and the write share one
// transaction; repeating an exit of the same process is a no-op. It reports whether it
// recorded the exit.
// ponytail: "done" is matched by message text, which is inferred state (motif 1), until
// messages carry a structured kind (R35).
func (l *Logbook) RecordExit(ctx context.Context, to, name string, spawn ID, proc, body string) (bool, error) {
	data, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return false, err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO entries (id, ts, kind, actor, subject, data)
SELECT ?1, ?2, 'message', 'bs', ?3, ?4 FROM entries s
WHERE s.id = ?5 AND s.kind = 'spawned' AND s.subject = ?6
  AND s.rowid = (SELECT MAX(x.rowid) FROM entries x WHERE x.kind = 'spawned' AND x.subject = ?6)
  AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = s.id)
  AND NOT EXISTS (SELECT 1 FROM entries d WHERE d.kind = 'message' AND d.actor = ?6 AND d.rowid > s.rowid
                  AND substr(json_extract(d.data, '$.body'), 1, 6) = 'done: ')`,
		"exit:"+string(spawn)+":"+proc, time.Now().UTC().Format(tsLayout), to, string(data), string(spawn), name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
}
