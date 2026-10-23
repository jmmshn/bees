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

// ErrNoLogbook is a logbook file that doesn't exist, which Open never makes: a deleted
// logbook must not come back empty without a word (R48, bees-8.12, motif 14).
var ErrNoLogbook = errors.New("no logbook")

// Open opens the existing logbook at path, migrating an older schema (migrate); a missing
// file is ErrNoLogbook. Writes take SQLite's write lock up front (BEGIN IMMEDIATE) and wait
// on a busy timeout.
func Open(ctx context.Context, path string) (*Logbook, error) {
	switch _, err := os.Stat(path); {
	case errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("%w at %s", ErrNoLogbook, path)
	case err != nil:
		return nil, err
	}
	return open(ctx, path)
}

// Create opens the logbook at path, making its directory and the file, schema built,
// when missing (created true). The schema is built in a temp file beside it and linked
// into place, never replacing one there: of parallel first calls exactly one creates, and
// no reader ever sees a file without its table (bees-13.59, motif 8).
func Create(ctx context.Context, path string) (lb *Logbook, created bool, err error) {
	switch _, err = os.Stat(path); {
	case errors.Is(err, os.ErrNotExist):
		if created, err = build(ctx, path); err != nil {
			return nil, false, fmt.Errorf("make logbook %s: %w", path, err)
		}
	case err != nil:
		return nil, false, err
	}
	lb, err = open(ctx, path)
	return lb, created, err
}

// AtFirstWrite opens the logbook at path when it exists, else returns one that reads as
// empty and is made (Create) by its first write: bs queen and bs spawn, which start a
// project, open it so, and a command refused before it records anything leaves no file
// (ADR-0010 Amendment 4, OQ-K1). Created says whether that write made it.
func AtFirstWrite(ctx context.Context, path string) (*Logbook, error) {
	switch _, err := os.Stat(path); {
	case err == nil:
		return open(ctx, path)
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	db, err := empty(ctx)
	if err != nil {
		return nil, err
	}
	return &Logbook{db: db, path: path}, nil
}

// build makes the logbook file at path with the schema: built in <path>.<id>.tmp
// (journal_mode DELETE, so no -wal sits beside the temp), then linked into place. created
// is true for this call's file, false for one another call linked first.
func build(ctx context.Context, path string) (created bool, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil { // other local users must not read the mail
		return false, err
	}
	tmp := fmt.Sprintf("%s.%s.tmp", abs, newID(time.Now()))
	defer os.Remove(tmp)
	q := url.Values{}
	q.Add("_pragma", "journal_mode(DELETE)")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: tmp, RawQuery: q.Encode()}).String())
	if err != nil {
		return false, err
	}
	err = schema(ctx, db)
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return false, err
	}
	existed, err := linkNew(tmp, abs)
	return !existed && err == nil, err
}

// schema makes the version 2 table and indexes on db.
func schema(ctx context.Context, db execer) error {
	for _, q := range []string{table("entries"), indexes, fmt.Sprintf(`PRAGMA user_version = %d`, Version)} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// empty is an in-memory database holding the schema and no entries: what a logbook not
// made yet, or a file without its table, reads as.
func empty(ctx context.Context) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // each connection to :memory: is a database of its own
	if err := schema(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// open opens the logbook at path, making it if missing.
func open(ctx context.Context, path string) (*Logbook, error) {
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
	var kept string
	for kept, err = migrate(ctx, db, abs); busy(err) && time.Now().Before(deadline); kept, err = migrate(ctx, db, abs) {
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
	return &Logbook{db: db, KeptCopy: kept}, nil
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
// (Spawn.decode), never rewritten. An older bs can read and write the rebuilt table only
// until this bs writes a spawn: its decoder takes a spawn's workspace for a string, and
// this bs writes an object. kept is a copy already there, which was kept.
func migrate(ctx context.Context, db *sql.DB, path string) (kept string, err error) {
	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return "", err
	}
	if v >= Version {
		return "", nil
	}
	old, err := hasEntries(ctx, db)
	if err != nil {
		return "", err
	}
	if old {
		aside := fmt.Sprintf("%s.pre-%d", path, Version)
		existed, err := copyAside(ctx, db, aside)
		if err != nil {
			return "", fmt.Errorf("copy the logbook aside before migrating it: %w", err)
		}
		if existed {
			kept = aside
		}
	}
	// One connection: foreign_keys is per connection and can't change inside a transaction,
	// and the rebuild drops the table its own rows' ref points into.
	c, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return "", err
	}
	err = rebuild(ctx, c)
	// The connection goes back to the pool: a failed reset fails Open, which closes it.
	_, onErr := c.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`)
	return kept, errors.Join(err, onErr)
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

// copyAside writes a consistent copy of db to path, keeping an earlier copy there (existed
// true): a concurrent Open may have made one already, or an earlier migration.
func copyAside(ctx context.Context, db *sql.DB, path string) (existed bool, err error) {
	tmp := fmt.Sprintf("%s.%s.tmp", path, newID(time.Now()))
	defer os.Remove(tmp) // a failed VACUUM may leave part of it
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return false, err
	}
	return linkNew(tmp, path)
}

// linkNew puts tmp at path unless a file is there already (existed true), never
// replacing it: a hard link, else, where the file system has none, a rename after a look.
func linkNew(tmp, path string) (existed bool, err error) {
	switch err := os.Link(tmp, path); {
	case err == nil:
		return false, nil
	case errors.Is(err, os.ErrExist):
		return true, nil
	case errors.Is(err, os.ErrPermission), errors.Is(err, errors.ErrUnsupported):
		if _, err := os.Lstat(path); err == nil { // a file system without hard links
			return true, nil
		}
		return false, os.Rename(tmp, path)
	default:
		return false, err
	}
}

// OpenReadOnly opens the logbook at path for reading only: it makes no logbook,
// directory, schema or migration (SQLite may add its -shm and -wal beside it), so scanning
// every logbook for a session can't create, rebuild or split one. A missing file is an
// error. An unmigrated file reads too: every reader maps old entries. A file with no
// entries table (one an older bs was still making) reads as an empty logbook.
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
	if ok, err := hasEntries(ctx, db); err != nil || !ok {
		db.Close()
		if err != nil {
			return nil, fmt.Errorf("open logbook %s: %w", path, err)
		}
		if db, err = empty(ctx); err != nil {
			return nil, err
		}
	}
	return &Logbook{db: db}, nil
}

// SchemaVersion is the file's PRAGMA user_version.
func (l *Logbook) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := l.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v)
	return v, err
}
