package logbook

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/goal"
)

// DefaultSession is the herdr session of a pane whose env has no HERDR_SESSION, and of
// every entry written before entries named one.
const DefaultSession = "default"

// DefaultHarness is the harness of an entry written before entries named one.
const DefaultHarness = "claude_code"

// Pane is where herdr runs a session: the server (its --session), the workspace and the
// pane. Pane ids are per server (a herdr --session numbers from w1:p1), so a pane is
// only ever matched with its session (ADR-0010 identity).
type Pane struct {
	Session   string `json:"session"`
	Workspace string `json:"workspace"`
	Pane      string `json:"pane"`
}

// Workspace is the directory a worker works in. Kind is git_worktree, none or command.
type Workspace struct {
	Kind   string `json:"kind"`
	Dir    string `json:"dir"`
	Repo   string `json:"repo,omitempty"`   // the project dir the workspace was made from
	Branch string `json:"branch,omitempty"` // git_worktree: the worker's branch
	Base   string `json:"base,omitempty"`   // the ref the branch was cut from
}

// Spawn is a spawned entry: everything bs end, prune and a handoff read about a worker,
// frozen at spawn (ADR-0010). It says nothing about whether the worker is running.
type Spawn struct {
	ID        ID                `json:"-"`
	TS        time.Time         `json:"-"`
	Name      string            `json:"-"`
	By        string            `json:"-"` // who spawned it: the queen bs done reports to
	Bead      string            `json:"bead"`
	Label     string            `json:"label,omitempty"`
	Harness   string            `json:"harness"`
	Herdr     Pane              `json:"herdr"`
	Workspace Workspace         `json:"workspace"`
	Goal      goal.Goal         `json:"goal"`
	ConfigDir string            `json:"config_dir"`
	Record    map[string]string `json:"record,omitempty"` // the harness's Launch.Record
	Path      []string          `json:"path,omitempty"`   // dirs first on the worker's PATH
	// At is the bead as spawn found it, for the note and beads checks; nil on a spawn
	// recorded before bs froze it.
	At *BeadAt `json:"bead_at,omitempty"`
	// PruneIgnore is prune.ignore at spawn: the ignored files prune lets go with the
	// workspace. None on a spawn recorded before bs froze it, so prune keeps every one.
	PruneIgnore []string `json:"prune_ignore,omitempty"`
	Ended       bool     `json:"-"`
	Read        bool     `json:"-"` // the worker has read a message since this spawn, so it has a conversation
}

// BeadAt is a spawn's bead as spawn found it: every spawn freezes it, so a goal amended
// to note or beads still checks against the spawn (ADR-0010).
type BeadAt struct {
	Note     string   `json:"note"`     // sha256 of its notes and design, hex
	Children []string `json:"children"` // the beads under it
}

// Legacy reports whether s was spawned before goals.
func (s Spawn) Legacy() bool { return s.Goal.IsLegacy() }

// oldSpawn is a spawned entry's data as main's bs (v0) and the bees-12 pilot (v1) wrote
// it: workspace was the herdr workspace id, worktree the directory.
type oldSpawn struct {
	Bead      string            `json:"bead"`
	Label     string            `json:"label"`
	Workspace string            `json:"workspace"`
	Pane      string            `json:"pane"`
	Worktree  string            `json:"worktree"`
	Branch    string            `json:"branch"`
	Repo      string            `json:"repo"`
	Kind      string            `json:"workspace_kind"` // v1: "none", or "" for git_worktree
	Dir       string            `json:"dir"`            // v1, kind none
	Harness   string            `json:"harness"`
	Record    map[string]string `json:"record"`
	Path      []string          `json:"path"`
}

// decode fills s from a spawned entry's data. Data with no goal is an old spawn,
// mapped field by field and read as --goal legacy (ADR-0010 migration); its rows are
// never rewritten.
func (s *Spawn) decode(data string) error {
	var probe struct {
		Goal json.RawMessage `json:"goal"`
	}
	if err := json.Unmarshal([]byte(data), &probe); err != nil {
		return fmt.Errorf("spawn record %s: %w", s.ID, err)
	}
	if len(probe.Goal) > 0 && !bytes.Equal(probe.Goal, []byte("null")) {
		if err := json.Unmarshal([]byte(data), s); err != nil {
			return fmt.Errorf("spawn record %s: %w", s.ID, err)
		}
		return nil
	}
	var o oldSpawn
	if err := json.Unmarshal([]byte(data), &o); err != nil {
		return fmt.Errorf("spawn record %s (before goals): %w", s.ID, err)
	}
	s.Bead, s.Label, s.Record, s.Path = o.Bead, o.Label, o.Record, o.Path
	s.Harness = cmp.Or(o.Harness, DefaultHarness)
	s.Herdr = Pane{Session: DefaultSession, Workspace: o.Workspace, Pane: o.Pane}
	// An old base is the checkout the branch was cut from, a path, never a ref: left
	// empty, it reads as the repo's default branch.
	s.Workspace = Workspace{Kind: "git_worktree", Dir: o.Worktree, Repo: o.Repo, Branch: o.Branch}
	if o.Kind == "none" {
		s.Workspace.Kind, s.Workspace.Dir = "none", cmp.Or(o.Dir, o.Worktree)
	}
	s.Goal = goal.Legacy()
	return nil
}

// SpawnID, in a brief, is replaced with the spawn's id: a review goal's marker
// (bs:<spawn id>) names an id that exists only once the spawn is recorded.
const SpawnID = "{{spawn}}"

// ErrAlreadySpawned is a spawn of a worker whose last spawn hasn't ended.
var ErrAlreadySpawned = errors.New("already spawned and not ended")

// RecordSpawn records that by spawned s.Name and, in the same transaction, sends it
// brief (if any), so a crash can't leave a recorded worker without its brief. The brief
// refers to the spawn: once the spawn has ended, an unread brief is no longer unread.
func (l *Logbook) RecordSpawn(ctx context.Context, by string, s Spawn, brief string) (ID, error) {
	if len(s.Goal.Parts) == 0 {
		return "", goal.ErrNoGoal // a spawn with no goal would read back as legacy
	}
	var id ID
	err := l.write(ctx, func(tx *sql.Tx) error {
		// Checked again here, under the write lock: a second spawn racing the caller's
		// check must not record a twin (motif 7).
		var open bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM entries s WHERE s.kind = 'spawned' AND s.subject = ?
  AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = s.id))`, s.Name).Scan(&open); err != nil {
			return err
		}
		if open {
			return fmt.Errorf("%w: %s", ErrAlreadySpawned, s.Name)
		}
		var err error
		if id, err = insertOn(ctx, tx, entry{kind: "spawned", actor: by, subject: s.Name, data: s}); err != nil {
			return err
		}
		if brief != "" {
			brief = strings.ReplaceAll(brief, SpawnID, string(id))
			_, err = insertOn(ctx, tx, entry{kind: "message", actor: by, subject: s.Name, ref: id, data: map[string]string{"body": brief}})
		}
		return err
	})
	return id, err
}

// spawnCols selects a spawned entry s with whether it has ended and whether its worker
// has read a message since.
const spawnCols = `s.id, s.ts, s.subject, s.actor, s.data,
  EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = s.id),
  EXISTS (SELECT 1 FROM entries r WHERE r.kind = 'received' AND r.actor = s.subject AND r.rowid > s.rowid)`

type scanner interface{ Scan(dest ...any) error }

func scanSpawn(r scanner) (Spawn, error) {
	var s Spawn
	var data, ts string
	if err := r.Scan(&s.ID, &ts, &s.Name, &s.By, &data, &s.Ended, &s.Read); err != nil {
		return Spawn{}, err
	}
	var err error
	if s.TS, err = time.Parse(tsLayout, ts); err != nil {
		return Spawn{}, fmt.Errorf("spawn record %s: %w", s.ID, err)
	}
	return s, s.decode(data)
}

// LatestSpawn returns the most recent spawn of the named worker; ok is false when the
// worker was never spawned.
func (l *Logbook) LatestSpawn(ctx context.Context, name string) (Spawn, bool, error) {
	s, err := scanSpawn(l.db.QueryRowContext(ctx, `SELECT `+spawnCols+` FROM entries s WHERE s.kind = 'spawned' AND s.subject = ? ORDER BY s.rowid DESC LIMIT 1`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Spawn{}, false, nil
	}
	return s, err == nil, err
}

// FirstSpawnAt is when the first spawn that recorded dir as its workspace was recorded:
// a workspace commit is one made there since (ADR-0010). ok is false when none did.
func (l *Logbook) FirstSpawnAt(ctx context.Context, dir string) (t time.Time, ok bool, err error) {
	var ts sql.NullString
	err = l.db.QueryRowContext(ctx, `SELECT MIN(ts) FROM entries WHERE kind = 'spawned'
  AND coalesce(json_extract(data, '$.workspace.dir'), json_extract(data, '$.worktree'), json_extract(data, '$.dir')) = ?`, dir).Scan(&ts)
	if err != nil || !ts.Valid {
		return time.Time{}, false, err
	}
	t, err = time.Parse(tsLayout, ts.String)
	return t, err == nil, err
}

// Spawn returns the spawned entry id.
func (l *Logbook) Spawn(ctx context.Context, id ID) (Spawn, error) {
	return scanSpawn(l.db.QueryRowContext(ctx, `SELECT `+spawnCols+` FROM entries s WHERE s.kind = 'spawned' AND s.id = ?`, string(id)))
}

// LatestSpawns returns each worker's latest spawn, by name, ended or not.
func (l *Logbook) LatestSpawns(ctx context.Context) ([]Spawn, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT `+spawnCols+` FROM entries s
WHERE s.kind = 'spawned'
  AND s.rowid = (SELECT MAX(x.rowid) FROM entries x WHERE x.kind = 'spawned' AND x.subject = s.subject)
ORDER BY s.subject`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Spawn
	for rows.Next() {
		s, err := scanSpawn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Unstarted names the workers whose latest spawn is open, was written before goals and
// has no start: sessions the old bs started, which this bs can neither ring nor see run.
func (l *Logbook) Unstarted(ctx context.Context) ([]string, error) {
	sps, err := l.LatestSpawns(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range sps {
		if s.Ended || !s.Legacy() {
			continue
		}
		if _, ok, err := l.LatestStart(ctx, s.ID); err != nil {
			return nil, err
		} else if !ok {
			out = append(out, s.Name)
		}
	}
	return out, nil
}

// Recorded is a session with an open entry: a worker's open spawn, or a queen's newest
// queen entry.
type Recorded struct {
	Name, Harness string
	Queen         bool
	Spawn         ID // a worker's spawned entry
	// Process is what that entry or its newest start recorded; a worker with no start
	// recorded yet has none.
	Process    Process
	HasProcess bool
	Repo       string // a queen's recorded repo (bees-12.43); a worker's is its spawn's
	// Session and Pane are where herdr runs it: entries from before sessions were recorded
	// are on DefaultSession.
	Session, Pane string
}

// Sessions returns every session with an open entry, the records identity is found from
// (ADR-0010 Amendment 4 Identity): each worker's open spawn and each queen's newest queen
// entry.
func (l *Logbook) Sessions(ctx context.Context) ([]Recorded, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT 0, s.subject, s.id, json_extract(s.data, '$.harness'),
       (SELECT st.data FROM entries st WHERE st.kind = 'started' AND st.ref = s.id ORDER BY st.rowid DESC LIMIT 1),
       coalesce(json_extract(s.data, '$.herdr.session'), 'default'),
       coalesce(json_extract(s.data, '$.herdr.pane'), json_extract(s.data, '$.pane'), '')
FROM entries s
WHERE s.kind = 'spawned'
  AND s.rowid = (SELECT MAX(x.rowid) FROM entries x WHERE x.kind = 'spawned' AND x.subject = s.subject)
  AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.kind = 'ended' AND e.ref = s.id)
UNION ALL
SELECT 1, q.subject, '', json_extract(q.data, '$.harness'), q.data,
       coalesce(json_extract(q.data, '$.session'), 'default'), coalesce(json_extract(q.data, '$.pane'), '')
FROM entries q
WHERE q.kind = 'queen'
  AND q.rowid = (SELECT MAX(x.rowid) FROM entries x WHERE x.kind = 'queen' AND x.subject = q.subject)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recorded
	for rows.Next() {
		var r Recorded
		var h, data sql.NullString
		if err := rows.Scan(&r.Queen, &r.Name, &r.Spawn, &h, &data, &r.Session, &r.Pane); err != nil {
			return nil, err
		}
		r.Harness = cmp.Or(h.String, DefaultHarness)
		if data.Valid {
			if err := json.Unmarshal([]byte(data.String), &r.Process); err != nil {
				return nil, fmt.Errorf("entry of %s: %w", r.Name, err)
			}
			r.HasProcess = r.Process.PID > 0
			if r.Queen {
				var q Queen
				if err := json.Unmarshal([]byte(data.String), &q); err != nil {
					return nil, fmt.Errorf("entry of %s: %w", r.Name, err)
				}
				r.Repo = q.Repo
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
