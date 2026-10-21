package logbook_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jmmshn/bees/internal/logbook"
)

// v0 is the schema every logbook had before ADR-0009's queen, started and done kinds.
const v0 = `
CREATE TABLE entries (
  id      TEXT PRIMARY KEY,
  ts      TEXT NOT NULL,
  kind    TEXT NOT NULL CHECK (kind IN ('message','received','rang','spawned','ended')),
  actor   TEXT NOT NULL,
  subject TEXT,
  ref     TEXT REFERENCES entries(id),
  data    TEXT CHECK (data IS NULL OR json_valid(data))
) STRICT;
CREATE INDEX k_subj ON entries (kind, subject, id);
CREATE INDEX k_ref  ON entries (kind, ref);
PRAGMA journal_mode = WAL;
INSERT INTO entries VALUES ('s1', '2026-10-01T00:00:00.000Z', 'spawned', 'queen-bees', 'bees-8', NULL, '{"bead":"bees-8","pane":"w1:p1"}');
INSERT INTO entries VALUES ('m1', '2026-10-01T00:00:01.000Z', 'message', 'queen-bees', 'bees-8', 's1', '{"body":"brief"}');
INSERT INTO entries VALUES ('m2', '2026-10-01T00:00:02.000Z', 'message', 'bees-8', 'queen-bees', NULL, '{"body":"hi"}');
`

// oldLogbook writes a version 0 logbook with a spawn, its brief and a reply.
func oldLogbook(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bees.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(v0); err != nil {
		t.Fatal(err)
	}
	return path
}

func kinds(t *testing.T, path string) (n int, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT count(*) FROM entries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return n, version
}

// bees-12.10: a version 0 logbook is copied aside, then rebuilt with the wider kind
// CHECK; every row survives, and a queen entry can then be written.
func TestOpenMigratesAVersion0LogbookAfterCopyingItAside(t *testing.T) {
	ctx := context.Background()
	path := oldLogbook(t)
	lb, err := logbook.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	if n, v := kinds(t, path+".pre-1"); n != 3 || v != 0 {
		t.Fatalf("copy aside: %d rows, version %d; want the 3 rows at version 0", n, v)
	}
	if fi, err := os.Stat(path + ".pre-1"); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("copy aside: %v, %v; want 0600", fi, err)
	}
	if n, v := kinds(t, path); n != 3 || v != 1 {
		t.Fatalf("migrated: %d rows, version %d; want 3 at version 1", n, v)
	}
	if sp, ok, err := lb.LatestSpawn(ctx, "bees-8"); err != nil || !ok || sp.Pane != "w1:p1" {
		t.Fatalf("spawn after migration = %+v, %v, %v", sp, ok, err)
	}
	if unread, err := lb.Unread(ctx, "bees-8"); err != nil || len(unread) != 1 || unread[0].Body != "brief" {
		t.Fatalf("unread after migration = %+v, %v", unread, err)
	}
	if _, err := lb.RecordQueen(ctx, logbook.Queen{Name: "queen-bees", Harness: "claude_code", Pane: "w2:p1", PID: 42, Start: "x"}); err != nil {
		t.Fatalf("queen entry after migration: %v", err)
	}
	if err := lb.RecordEnd(ctx, "queen-bees", "s1"); err != nil { // ref still enforced
		t.Fatal(err)
	}
}

// A second Open finds the work done, and many concurrent Opens of an old logbook migrate
// it once, each seeing every row.
func TestMigrationRunsOnceUnderConcurrentOpens(t *testing.T) {
	ctx := context.Background()
	path := oldLogbook(t)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() {
			lb, err := logbook.Open(ctx, path)
			if err == nil {
				lb.Close()
			}
			errs[i] = err
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n, v := kinds(t, path); n != 3 || v != 1 {
		t.Fatalf("after concurrent opens: %d rows, version %d", n, v)
	}
	left, _ := filepath.Glob(path + ".pre-1.*")
	if len(left) != 0 {
		t.Fatalf("temporary copies left: %v", left)
	}
}

// A new logbook is made at version 1 with no copy aside.
func TestANewLogbookStartsAtTheCurrentVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bees.db")
	lb, err := logbook.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	lb.Close()
	if _, v := kinds(t, path); v != 1 {
		t.Fatalf("version %d, want 1", v)
	}
	if _, err := os.Stat(path + ".pre-1"); !os.IsNotExist(err) {
		t.Fatalf("copy aside of a new logbook: %v", err)
	}
}

// The newest queen entry is the queen.
func TestLatestQueenIsTheNewest(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	if _, ok, err := lb.LatestQueen(ctx, "queen-bees"); ok || err != nil {
		t.Fatalf("before any: ok=%v err=%v", ok, err)
	}
	for _, pid := range []int{10, 20} {
		if _, err := lb.RecordQueen(ctx, logbook.Queen{Name: "queen-bees", Harness: "claude_code", Pane: "w1:p1", PID: pid, Start: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	if q, ok, err := lb.LatestQueen(ctx, "queen-bees"); err != nil || !ok || q.PID != 20 || q.Pane != "w1:p1" || q.Harness != "claude_code" {
		t.Fatalf("LatestQueen = %+v, %v, %v", q, ok, err)
	}
}
