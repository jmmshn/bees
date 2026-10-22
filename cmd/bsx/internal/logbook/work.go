package logbook

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/goal"
	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

// GoalChange is a goal entry: bs goal's amend of a live, incomplete goal, a waiver of one
// check, or the keeper's discard (ADR-0010).
type GoalChange struct {
	By      string     `json:"by"`
	Goal    *goal.Goal `json:"goal,omitempty"`  // an amend: the new resolved goal
	Waive   string     `json:"waive,omitempty"` // a waiver: the check no longer run
	Discard bool       `json:"discard,omitempty"`
}

// RecordGoal records change c to spawn's goal.
func (l *Logbook) RecordGoal(ctx context.Context, spawn ID, name string, c GoalChange) (ID, error) {
	return insert(ctx, l.db, entry{kind: "goal", actor: c.By, subject: name, ref: spawn, data: c})
}

// Effective is a spawn's goal as its goal entries left it.
type Effective struct {
	goal.Goal
	Waived    []string
	Discarded bool
}

// Goal returns sp's effective goal: its frozen goal, or the newest amend, with every
// waiver and any discard since (ADR-0010: read the record, never config).
func (l *Logbook) Goal(ctx context.Context, sp Spawn) (Effective, error) {
	e := Effective{Goal: sp.Goal}
	rows, err := l.db.QueryContext(ctx, `SELECT id, data FROM entries WHERE kind = 'goal' AND ref = ? ORDER BY rowid`, string(sp.ID))
	if err != nil {
		return Effective{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return Effective{}, err
		}
		var c GoalChange
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return Effective{}, fmt.Errorf("goal entry %s: %w", id, err)
		}
		switch {
		case c.Discard:
			e.Discarded = true
		case c.Goal != nil:
			e.Goal, e.Waived = *c.Goal, nil // an amend re-resolves: earlier waivers were for the old goal
		case c.Waive != "" && !slices.Contains(e.Waived, c.Waive):
			e.Waived = append(e.Waived, c.Waive)
		}
	}
	return e, rows.Err()
}

// CheckResult is one check's outcome. A check that couldn't run has Err set and is
// neither pass nor fail ("couldn't look" is never "no", Rule G).
type CheckResult struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"` // what bs found, or why it failed
	Err    string `json:"error,omitempty"`  // why it couldn't run
	// Pending: it failed only because CI is still running; bs done says to re-run, with an
	// exit code of its own.
	Pending bool `json:"pending,omitempty"`
}

// Done is a done entry, a worker's handoff (bs done): the PRs handed off so far, or a
// verdict, and every check's result. A second bs done is a new entry; PRs accumulate in
// the newest. It is addressed to Queen, so it reads as her unread mail.
type Done struct {
	ID     ID            `json:"-"`
	Spawn  ID            `json:"-"`
	Name   string        `json:"-"`
	Queen  string        `json:"-"`
	TS     time.Time     `json:"-"`
	Bead   string        `json:"bead"`
	PRs    []string      `json:"prs,omitempty"`
	Answer string        `json:"answer,omitempty"`
	Report string        `json:"report,omitempty"`
	Checks []CheckResult `json:"checks,omitempty"`
	// Background is what bs done --force found running; Unchecked why it couldn't look.
	Background []proc.Background `json:"background,omitempty"`
	Unchecked  string            `json:"unchecked,omitempty"`
	// PR is a pilot (v1) done's single PR; decode moves it into PRs.
	PR string `json:"pr,omitempty"`
}

// Passed reports whether every check d ran passed.
func (d Done) Passed() bool {
	return !slices.ContainsFunc(d.Checks, func(c CheckResult) bool { return !c.Pass })
}

// Text is d as its queen's inbox shows it.
func (d Done) Text() string {
	s := "done: " + d.Bead
	if prs := d.prs(); len(prs) > 0 {
		s += " PR " + strings.Join(prs, " ")
	}
	if d.Answer != "" {
		s += " answer " + d.Answer
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
	if d.Unchecked != "" {
		s += "\n(reported with bs done --force; " + d.Unchecked + ")"
	}
	return s
}

func (d Done) prs() []string {
	if d.PR != "" && !slices.Contains(d.PRs, d.PR) {
		return append([]string{d.PR}, d.PRs...)
	}
	return d.PRs
}

// RecordDone records done d of the worker whose spawned entry is d.Spawn, addressed to
// d.Queen.
func (l *Logbook) RecordDone(ctx context.Context, d Done) (ID, error) {
	return insert(ctx, l.db, entry{kind: "done", actor: d.Name, subject: d.Queen, ref: d.Spawn, data: d})
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
	d.PRs, d.PR = d.prs(), ""
	return d, true, nil
}

// Landed is a landed entry: the goal is complete (ADR-0010). By is end, prune or done.
type Landed struct {
	By     string   `json:"by"`
	Checks []string `json:"checks"` // the complete checks that passed
}

// RecordLanded records that spawn's goal is complete. A spawn lands once: a second call
// is a no-op.
func (l *Logbook) RecordLanded(ctx context.Context, spawn ID, name, actor string, ld Landed) error {
	if ld.Checks == nil {
		ld.Checks = []string{}
	}
	_, err := insert(ctx, l.db, entry{id: "land:" + string(spawn), kind: "landed", actor: actor, subject: name, ref: spawn, data: ld})
	return err
}

// LandedOf returns spawn's landed entry; ok is false while its goal isn't complete.
func (l *Logbook) LandedOf(ctx context.Context, spawn ID) (ld Landed, ok bool, err error) {
	_, ok, err = l.newest(ctx, "landed", spawn, &ld)
	return ld, ok, err
}

// Ended is an ended entry's data; one written before ADR-0010 has none.
type Ended struct {
	Force      bool `json:"force,omitempty"`
	Incomplete bool `json:"incomplete,omitempty"` // bs end --incomplete: prune completes it later
}

// RecordEnd records that by ended the given spawn. Ending it again is a no-op.
func (l *Logbook) RecordEnd(ctx context.Context, by string, spawn ID, e Ended) error {
	return l.write(ctx, func(tx *sql.Tx) error {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT subject FROM entries WHERE id = ? AND kind = 'spawned'`, string(spawn)).Scan(&name); err != nil {
			return fmt.Errorf("spawn %s: %w", spawn, err)
		}
		_, err := insert(ctx, tx, entry{id: "end:" + string(spawn), kind: "ended", actor: by, subject: name, ref: spawn, data: e})
		return err
	})
}

// EndOf returns spawn's ended entry; ok is false while it is open.
func (l *Logbook) EndOf(ctx context.Context, spawn ID) (e Ended, ok bool, err error) {
	_, ok, err = l.newest(ctx, "ended", spawn, &e)
	return e, ok, err
}
