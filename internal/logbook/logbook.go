// Package logbook is the append-only record of every message in peons (ADR-0001, ADR-0002),
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

// Unread returns the messages addressed to session that have no receipt from it, in the order they were stored.
func (l *Logbook) Unread(ctx context.Context, session string) ([]Message, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT m.id, m.ts, m.actor, m.subject, json_extract(m.data, '$.body') FROM entries m
WHERE m.kind = 'message' AND m.subject = ?1
  AND NOT EXISTS (SELECT 1 FROM entries r
                  WHERE r.kind = 'received' AND r.ref = m.id AND r.actor = ?1)
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

// Spawn records a peon pns started: its herdr workspace is the key pns end needs once
// the agent is gone (ADR-0006). It says nothing about whether the peon is running.
type Spawn struct {
	ID        ID     `json:"-"`
	Name      string `json:"-"`
	Bead      string `json:"bead"`
	Workspace string `json:"workspace"`
	Pane      string `json:"pane"`
	Worktree  string `json:"worktree"`
	Branch    string `json:"branch"`
	Ended     bool   `json:"-"`
}

// RecordSpawn records that by spawned s.Name.
func (l *Logbook) RecordSpawn(ctx context.Context, by string, s Spawn) (ID, error) {
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

// LatestSpawn returns the most recent spawn of the named peon, and whether it has ended.
// ok is false when the peon was never spawned.
func (l *Logbook) LatestSpawn(ctx context.Context, name string) (s Spawn, ok bool, err error) {
	var data string
	err = l.db.QueryRowContext(ctx, `
SELECT s.id, s.data, EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = s.id)
FROM entries s WHERE s.kind = 'spawned' AND s.subject = ? ORDER BY s.rowid DESC LIMIT 1`, name,
	).Scan(&s.ID, &data, &s.Ended)
	if errors.Is(err, sql.ErrNoRows) {
		return Spawn{}, false, nil
	}
	if err != nil {
		return Spawn{}, false, err
	}
	s.Name = name
	return s, true, json.Unmarshal([]byte(data), &s)
}
