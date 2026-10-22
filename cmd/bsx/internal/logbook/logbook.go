// Package logbook is the append-only record of every message and fact in bees (ADR-0001,
// ADR-0002), stored in a local SQLite file (ADR-0005), at schema version 2 (ADR-0010).
package logbook

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ID identifies an entry. Message ids sort by creation time.
type ID string

// Logbook is an open logbook file.
type Logbook struct{ db *sql.DB }

// Close closes the logbook.
func (l *Logbook) Close() error { return l.db.Close() }

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// execer is a *sql.DB or a *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
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

// entry is one row to insert. An empty id gets a new one.
type entry struct {
	id, kind, actor, subject string
	ref                      ID
	data                     any // marshalled to JSON; nil stores NULL
}

// insert writes e on db and returns its id. An id given (a deterministic one) that is
// already there is kept, and the call is a no-op (INSERT OR IGNORE).
func insert(ctx context.Context, db execer, e entry) (ID, error) {
	now := time.Now().UTC()
	id := ID(e.id)
	if id == "" {
		id = newID(now)
	}
	var data sql.NullString
	if e.data != nil {
		b, err := json.Marshal(e.data)
		if err != nil {
			return "", err
		}
		data = sql.NullString{String: string(b), Valid: true}
	}
	var ref sql.NullString
	if e.ref != "" {
		ref = sql.NullString{String: string(e.ref), Valid: true}
	}
	var subject sql.NullString
	if e.subject != "" {
		subject = sql.NullString{String: e.subject, Valid: true}
	}
	_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO entries (id, ts, kind, actor, subject, ref, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(id), now.Format(tsLayout), e.kind, e.actor, subject, ref, data)
	return id, err
}

// write runs f in one write transaction (BEGIN IMMEDIATE).
func (l *Logbook) write(ctx context.Context, f func(tx *sql.Tx) error) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// newest scans the data of the newest entry of kind referring to ref into v, with its
// id; ok is false when there is none.
func (l *Logbook) newest(ctx context.Context, kind string, ref ID, v any) (id ID, ok bool, err error) {
	var data sql.NullString
	err = l.db.QueryRowContext(ctx, `SELECT id, data FROM entries WHERE kind = ? AND ref = ? ORDER BY rowid DESC LIMIT 1`, kind, string(ref)).Scan(&id, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if data.Valid && v != nil {
		if err := json.Unmarshal([]byte(data.String), v); err != nil {
			return "", false, fmt.Errorf("%s entry %s: %w", kind, id, err)
		}
	}
	return id, true, nil
}
