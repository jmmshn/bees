package logbook

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Message is one entry addressed from one session to another, or a done entry as its
// queen's inbox shows it.
type Message struct {
	ID          ID
	From, To    string
	FromProject string // the sender's project when it wrote from another one's logbook; "" otherwise
	Body        string
	TS          time.Time
}

// Send stores a message from one session to another and returns its id.
func (l *Logbook) Send(ctx context.Context, from, to, body string) (ID, error) {
	return l.SendFrom(ctx, from, "", to, body)
}

// SendFrom is Send from a session of project fromProject, writing into another project's
// logbook: the entry records it, so the reply needs no project read back out of a name
// (motif 3). "" is the logbook's own project.
func (l *Logbook) SendFrom(ctx context.Context, from, fromProject, to, body string) (ID, error) {
	d := map[string]string{"body": body}
	if fromProject != "" {
		d["from_project"] = fromProject
	}
	return insert(ctx, l.db, entry{kind: "message", actor: from, subject: to, data: d})
}

// forOpenSpawn scopes a message m to its recipient's open spawn, at read time (bees-13.9):
// a name ever spawned reads only what was stored after its latest spawn, while that spawn
// is open, so mail for an earlier spawn, or sent between spawns, never reaches a later one
// (migrated mail too); a name never spawned (a queen) reads all of its own.
const forOpenSpawn = `
  (NOT EXISTS (SELECT 1 FROM entries x WHERE x.kind = 'spawned' AND x.subject = m.subject)
   OR EXISTS (SELECT 1 FROM entries s WHERE s.kind = 'spawned' AND s.subject = m.subject AND s.rowid < m.rowid
              AND s.rowid = (SELECT MAX(x.rowid) FROM entries x WHERE x.kind = 'spawned' AND x.subject = m.subject)
              AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = s.id)))`

// unread is the anti-join over receipts (ADR-0002): messages and done entries addressed
// to ?1 with no receipt from it, scoped to its open spawn.
const unread = `
FROM entries m
WHERE m.kind IN ('message', 'done') AND m.subject = ?1
  AND NOT EXISTS (SELECT 1 FROM entries r WHERE r.kind = 'received' AND r.ref = m.id AND r.actor = ?1)
  AND` + forOpenSpawn

// Unread returns session's unread messages and done entries in the order they were stored.
func (l *Logbook) Unread(ctx context.Context, session string) ([]Message, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT m.id, m.ts, m.actor, m.subject, coalesce(json_extract(m.data, '$.from_project'), ''), m.kind, m.data `+unread+` ORDER BY m.rowid`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var ts, kind string
		var data sql.NullString
		if err := rows.Scan(&m.ID, &ts, &m.From, &m.To, &m.FromProject, &kind, &data); err != nil {
			return nil, err
		}
		if err := m.decode(kind, data.String); err != nil {
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
		d.Name = m.From
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

// UnreadCounts returns every session the logbook names (a writer, recipient or worker)
// with its unread count, which is 0 for sessions with nothing unread.
func (l *Logbook) UnreadCounts(ctx context.Context) (map[string]int, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT name, SUM(unread) FROM (
  SELECT actor AS name, 0 AS unread FROM entries
  UNION ALL SELECT subject, 0 FROM entries WHERE kind = 'spawned'
  UNION ALL SELECT m.subject, NOT EXISTS (SELECT 1 FROM entries r
                                         WHERE r.kind = 'received' AND r.ref = m.id AND r.actor = m.subject)
            FROM entries m WHERE m.kind IN ('message', 'done') AND`+forOpenSpawn+`
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
	return l.write(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := insert(ctx, tx, entry{id: "rcv:" + string(id) + ":" + session, kind: "received", actor: session, ref: id}); err != nil {
				return err
			}
		}
		return nil
	})
}
