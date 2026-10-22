package logbook

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jmmshn/bees/internal/proc"
)

// Process is a process as bs records it: a pid with its start time.
type Process = proc.Process

// Queen is a queen entry, written by bs queen before it execs the harness: the herdr pane
// she runs in and her process, whose pid and start time exec keeps. A queen entry is
// never closed: the newest is the queen.
type Queen struct {
	ID      ID     `json:"-"`
	Name    string `json:"-"`
	Harness string `json:"harness"`
	Session string `json:"session"` // herdr session; "" in a pilot (v1) entry reads as DefaultSession
	Pane    string `json:"pane"`
	Process
	Exe string `json:"exe,omitempty"`
}

// RecordQueen records queen q, unless the newest queen entry of q.Name is one alive
// reports running: then it returns that entry and ok false. The check and the record
// share one write transaction, so of two queens starting at once only one is recorded
// (motif 7).
func (l *Logbook) RecordQueen(ctx context.Context, q Queen, alive func(Queen) (bool, error)) (running Queen, ok bool, err error) {
	err = l.write(ctx, func(tx *sql.Tx) error {
		prev, found, err := latestQueen(ctx, tx, q.Name)
		if err != nil {
			return err
		}
		if found {
			switch up, err := alive(prev); {
			case err != nil:
				return err
			case up:
				running = prev
				return nil
			}
		}
		ok = true
		_, err = insert(ctx, tx, entry{kind: "queen", actor: q.Name, subject: q.Name, data: q})
		return err
	})
	if err != nil {
		return Queen{}, false, err
	}
	return running, ok, nil
}

// LatestQueen returns the newest queen entry for name; ok is false when there is none.
func (l *Logbook) LatestQueen(ctx context.Context, name string) (Queen, bool, error) {
	return latestQueen(ctx, l.db, name)
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
	q.Session = cmp.Or(q.Session, DefaultSession)
	q.Harness = cmp.Or(q.Harness, DefaultHarness)
	return q, true, nil
}

// Start is a started entry: the harness process a worker's start confirmation (or a
// later heal, Late) found in its pane, the one fact behind its liveness (ADR-0010).
// Baseline is its descendants when first seen, for the background-work gate;
// BaselineErr says why they couldn't be read, so the gate fails closed.
type Start struct {
	ID    ID     `json:"-"`
	Spawn ID     `json:"-"`
	Name  string `json:"-"`
	Process
	Harness     string    `json:"harness"`
	Exe         string    `json:"exe,omitempty"`
	Baseline    []Process `json:"baseline"`
	BaselineErr string    `json:"baseline_error,omitempty"`
	Late        bool      `json:"late,omitempty"`
}

// LatestStart returns the newest start recorded for spawn; ok is false when the spawn has
// none (never confirmed, or spawned before bs recorded starts).
func (l *Logbook) LatestStart(ctx context.Context, spawn ID) (s Start, ok bool, err error) {
	s.ID, ok, err = l.newest(ctx, "started", spawn, &s)
	s.Spawn = spawn
	s.Harness = cmp.Or(s.Harness, DefaultHarness)
	return s, ok, err
}

// RecordStartOnce records start s unless spawn s.Spawn already has one: then it returns
// that start and recorded false. The check and the record share one write transaction,
// so two heals racing record one start (motif 7).
func (l *Logbook) RecordStartOnce(ctx context.Context, s Start) (got Start, recorded bool, err error) {
	if s.Baseline == nil {
		s.Baseline = []Process{}
	}
	err = l.write(ctx, func(tx *sql.Tx) error {
		var data string
		switch err := tx.QueryRowContext(ctx, `SELECT id, data FROM entries WHERE kind = 'started' AND ref = ? ORDER BY rowid DESC LIMIT 1`, string(s.Spawn)).Scan(&got.ID, &data); {
		case err == nil:
			got.Spawn, got.Name = s.Spawn, s.Name
			return json.Unmarshal([]byte(data), &got)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		id, err := insert(ctx, tx, entry{kind: "started", actor: s.Name, subject: s.Name, ref: s.Spawn, data: s})
		if err != nil {
			return err
		}
		got, recorded = s, true
		got.ID = id
		return nil
	})
	got.Harness = cmp.Or(got.Harness, DefaultHarness)
	return got, recorded, err
}
