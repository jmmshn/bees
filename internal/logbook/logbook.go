// Package logbook is the append-only record of every message in peons (ADR-0001, ADR-0002),
// stored in a local SQLite file (ADR-0005).
package logbook

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
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
  kind    TEXT NOT NULL CHECK (kind IN ('message','received','rang')),
  actor   TEXT NOT NULL,
  subject TEXT,
  ref     TEXT REFERENCES entries(id),
  data    TEXT CHECK (data IS NULL OR json_valid(data))
) STRICT;
CREATE INDEX IF NOT EXISTS k_subj ON entries (kind, subject, id);
CREATE INDEX IF NOT EXISTS k_ref  ON entries (kind, ref);
`

// Open opens the logbook at path, creating the file and its directory if missing.
// Writes take SQLite's write lock up front (BEGIN IMMEDIATE) and wait on a busy timeout.
func Open(ctx context.Context, path string) (*Logbook, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open logbook %s: %w", path, err)
	}
	return &Logbook{db: db}, nil
}

// Close closes the logbook.
func (l *Logbook) Close() error { return l.db.Close() }

// Init creates the entries table and its indexes if they don't exist.
func (l *Logbook) Init(ctx context.Context) error {
	_, err := l.db.ExecContext(ctx, schema)
	return err
}

// Send stores a message from one session to another and returns its id.
func (l *Logbook) Send(ctx context.Context, from, to, body string) (ID, error) {
	now := time.Now().UTC()
	id := newID(now)
	data, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return "", err
	}
	_, err = l.db.ExecContext(ctx,
		`INSERT INTO entries (id, ts, kind, actor, subject, data) VALUES (?, ?, 'message', ?, ?, ?)`,
		string(id), now.Format(time.RFC3339Nano), from, to, string(data))
	if err != nil {
		return "", err
	}
	return id, nil
}

// Unread returns the messages addressed to session that have no receipt from it, oldest first.
func (l *Logbook) Unread(ctx context.Context, session string) ([]Message, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT m.id, m.ts, m.actor, m.subject, json_extract(m.data, '$.body') FROM entries m
WHERE m.kind = 'message' AND m.subject = ?1
  AND NOT EXISTS (SELECT 1 FROM entries r
                  WHERE r.kind = 'received' AND r.ref = m.id AND r.actor = ?1)
ORDER BY m.id`, session)
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
		if m.TS, err = time.Parse(time.RFC3339Nano, ts); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkRead records that session was shown the given messages, in one transaction.
// A receipt's id is derived from the message and session, so repeating it is a no-op.
func (l *Logbook) MarkRead(ctx context.Context, session string, ids []ID) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO entries (id, ts, kind, actor, ref) VALUES (?, ?, 'received', ?, ?)`,
			"rcv:"+string(id)+":"+session, ts, session, string(id)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// newID returns a 26-character id that sorts by time: 13 hex digits of Unix milliseconds,
// then 13 hex digits of randomness.
func newID(t time.Time) ID {
	var b [8]byte
	rand.Read(b[:])
	return ID(fmt.Sprintf("%013x%013x", t.UnixMilli(), binary.BigEndian.Uint64(b[:])>>12))
}
