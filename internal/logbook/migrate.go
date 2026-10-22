package logbook

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Kinds is every entry kind the CHECK allows. It only ever grows (ADR-0010): rang is kept
// though nothing writes it, so a table from any older bs still fits.
var Kinds = []string{"message", "received", "rang", "spawned", "ended", "queen", "started", "done", "goal", "landed"}

// table is the DDL of the entries table of schema version 2, named name.
func table(name string) string {
	return `CREATE TABLE IF NOT EXISTS ` + name + ` (
  id      TEXT PRIMARY KEY,
  ts      TEXT NOT NULL,
  kind    TEXT NOT NULL CHECK (kind IN ('message','received','rang','spawned','ended','queen','started','done','goal','landed')),
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

// Version is the schema's PRAGMA user_version: 0 is main's bs before ADR-0009, 1 the
// bees-12 pilot's (queen, started, done), 2 ADR-0010's (goal, landed).
const Version = 2

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

// migrate brings the schema to Version. A new file gets the table as it is now. A version
// 0 or 1 file is copied aside to <path>.pre-2 first (motif 14), then its table is rebuilt
// once with the wider kind CHECK, which SQLite can't alter in place: inside BEGIN
// IMMEDIATE, the write lock every writer takes, so a concurrent Open waits and then finds
// the work done. Rows move verbatim: an old spawn's data is mapped when it is read
// (Spawn.decode), never rewritten. An older bs keeps working on the rebuilt table: its
// reads select by kind and its writes satisfy the wider CHECK.
func migrate(ctx context.Context, db *sql.DB, path string) error {
	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= Version {
		return nil
	}
	old, err := hasEntries(ctx, db)
	if err != nil {
		return err
	}
	if old {
		if err := copyAside(ctx, db, fmt.Sprintf("%s.pre-%d", path, Version)); err != nil {
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

// rebuild makes the version 2 table on c, unless another Open did while c waited.
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
	if v >= Version {
		return nil // another Open migrated while this one waited for the lock
	}
	old, err := hasEntries(ctx, tx)
	if err != nil {
		return err
	}
	stmts := []string{table("entries")}
	if old {
		stmts = []string{
			table("entries_v2"),
			`INSERT INTO entries_v2 (id, ts, kind, actor, subject, ref, data) SELECT id, ts, kind, actor, subject, ref, data FROM entries ORDER BY rowid`,
			`DROP TABLE entries`,
			`ALTER TABLE entries_v2 RENAME TO entries`,
		}
	}
	stmts = append(stmts, indexes, fmt.Sprintf(`PRAGMA user_version = %d`, Version))
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

// OpenReadOnly opens the logbook at path for reading only: it makes no logbook,
// directory, schema or migration (SQLite may add its -shm and -wal beside it), so scanning
// every logbook for a pane can't create, rebuild or split one. A missing file is an error.
// An unmigrated file reads too: every reader maps old entries.
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

// SchemaVersion is the file's PRAGMA user_version.
func (l *Logbook) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := l.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v)
	return v, err
}
