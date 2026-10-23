package logbook_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/logbook"
)

// v0 is main's schema before ADR-0009, with a spawn, its brief, a reply and a receipt.
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
INSERT INTO entries VALUES ('s1', '2026-10-01T00:00:00.000Z', 'spawned', 'queen-bees', 'bees-8', NULL,
  '{"bead":"bees-8","label":"logbook","workspace":"w2F","pane":"w2F:p1","worktree":"/r/bees-WT/bees-8","branch":"bees-8","repo":"/r/bees","base":"/r/bees"}');
INSERT INTO entries VALUES ('m1', '2026-10-01T00:00:01.000Z', 'message', 'queen-bees', 'bees-8', 's1', '{"body":"brief"}');
INSERT INTO entries VALUES ('m2', '2026-10-01T00:00:02.000Z', 'message', 'bees-8', 'queen-bees', NULL, '{"body":"hi"}');
INSERT INTO entries VALUES ('r1', '2026-10-01T00:00:03.000Z', 'rang', 'bees-8', 'queen-bees', 'm2', NULL);
`

// v1 is the bees-12 pilot's schema, with a none-kind spawn on another harness, its
// start, and a done with the pilot's single pr.
const v1 = `
CREATE TABLE entries (
  id      TEXT PRIMARY KEY,
  ts      TEXT NOT NULL,
  kind    TEXT NOT NULL CHECK (kind IN ('message','received','rang','spawned','ended','queen','started','done')),
  actor   TEXT NOT NULL,
  subject TEXT,
  ref     TEXT REFERENCES entries(id),
  data    TEXT CHECK (data IS NULL OR json_valid(data))
) STRICT;
CREATE INDEX k_subj ON entries (kind, subject, id);
CREATE INDEX k_ref  ON entries (kind, ref);
PRAGMA journal_mode = WAL;
PRAGMA user_version = 1;
INSERT INTO entries VALUES ('q1', '2026-10-20T00:00:00.000Z', 'queen', 'queen-toy', 'queen-toy', NULL, '{"harness":"claude_code","pane":"w1:p1","pid":10,"start":"100"}');
INSERT INTO entries VALUES ('s1', '2026-10-20T00:00:01.000Z', 'spawned', 'queen-toy', 'toy-1', NULL,
  '{"bead":"toy-1","workspace":"w3","pane":"w3:p1","worktree":"","branch":"","repo":"/r/toy","workspace_kind":"none","dir":"/r/toy","harness":"agy","record":{"conversation":"c1"}}');
INSERT INTO entries VALUES ('t1', '2026-10-20T00:00:02.000Z', 'started', 'toy-1', 'toy-1', 's1', '{"pid":20,"start":"200","harness":"agy","baseline":[]}');
INSERT INTO entries VALUES ('d1', '2026-10-20T00:00:03.000Z', 'done', 'toy-1', 'queen-toy', 's1', '{"bead":"toy-1","pr":"https://github.com/o/r/pull/7"}');
`

func write(t *testing.T, ddl string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bees.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	return path
}

func shape(t *testing.T, path string) (rows int, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT count(*) FROM entries`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return rows, version
}

func open(t *testing.T, path string) *logbook.Logbook {
	t.Helper()
	lb, _, err := logbook.Create(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lb.Close() })
	return lb
}

// A v0 logbook (main's) is copied aside to .pre-2, rebuilt with the CHECK grown (rang
// kept), and its old spawn reads as --goal legacy with its data mapped explicitly.
func TestOpenMigratesAVersion0Logbook(t *testing.T) {
	ctx := context.Background()
	path := write(t, v0)
	lb := open(t, path)
	if n, v := shape(t, path+".pre-2"); n != 4 || v != 0 {
		t.Fatalf("copy aside: %d rows, version %d; want the 4 rows at version 0", n, v)
	}
	if fi, err := os.Stat(path + ".pre-2"); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("copy aside: %v, %v; want 0600", fi, err)
	}
	if n, v := shape(t, path); n != 4 || v != logbook.Version {
		t.Fatalf("migrated: %d rows, version %d; want 4 at version %d", n, v, logbook.Version)
	}
	sp, ok, err := lb.LatestSpawn(ctx, "bees-8")
	if err != nil || !ok {
		t.Fatalf("spawn after migration: %v, %v", ok, err)
	}
	want := logbook.Spawn{
		Bead: "bees-8", Label: "logbook", Harness: "claude_code", By: "queen-bees",
		Herdr:     logbook.Pane{Session: "default", Workspace: "w2F", Pane: "w2F:p1"},
		Workspace: logbook.Workspace{Kind: "git_worktree", Dir: "/r/bees-WT/bees-8", Repo: "/r/bees", Branch: "bees-8"},
	}
	if !sp.Legacy() || sp.Bead != want.Bead || sp.Label != want.Label || sp.Harness != want.Harness || sp.By != want.By ||
		sp.Herdr != want.Herdr || sp.Workspace != want.Workspace {
		t.Fatalf("legacy spawn = %+v\nwant %+v", sp, want)
	}
	if unread, err := lb.Unread(ctx, "bees-8"); err != nil || len(unread) != 1 || unread[0].Body != "brief" {
		t.Fatalf("unread after migration = %+v, %v", unread, err)
	}
	// Every new kind can be written, and the foreign key survived the rebuild.
	if _, err := lb.RecordGoal(ctx, sp.ID, sp.Name, logbook.GoalChange{By: "keeper", Discard: true}); err != nil {
		t.Fatal(err)
	}
	if err := lb.RecordLanded(ctx, sp.ID, sp.Name, "queen-bees", logbook.Landed{By: "prune"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO entries (id, ts, kind, actor, ref) VALUES ('x', 't', 'ended', 'q', 'no-such-entry')`); err == nil {
		t.Fatal("an entry referring to no entry was stored: the rebuilt table lost its foreign key")
	}
	if _, err := db.Exec(`INSERT INTO entries (id, ts, kind, actor) VALUES ('y', 't', 'rang', 'q')`); err != nil {
		t.Fatalf("rang is no longer allowed: %v", err)
	}
}

// A v1 logbook (the pilot's, as the real bees.db is) reaches v2 the same way: its none
// spawn maps dir and harness, its start and done read back, its single pr is a PR.
func TestOpenMigratesAVersion1Logbook(t *testing.T) {
	ctx := context.Background()
	path := write(t, v1)
	lb := open(t, path)
	if n, v := shape(t, path+".pre-2"); n != 4 || v != 1 {
		t.Fatalf("copy aside: %d rows, version %d", n, v)
	}
	if _, v := shape(t, path); v != logbook.Version {
		t.Fatalf("version %d", v)
	}
	sp, _, err := lb.LatestSpawn(ctx, "toy-1")
	if err != nil {
		t.Fatal(err)
	}
	if !sp.Legacy() || sp.Workspace.Kind != "none" || sp.Workspace.Dir != "/r/toy" || sp.Harness != "agy" || sp.Record["conversation"] != "c1" || sp.Herdr.Pane != "w3:p1" {
		t.Fatalf("v1 spawn = %+v", sp)
	}
	if st, ok, err := lb.LatestStart(ctx, sp.ID); err != nil || !ok || st.PID != 20 || st.Start != "200" {
		t.Fatalf("start = %+v, %v, %v", st, ok, err)
	}
	if d, ok, err := lb.LatestDone(ctx, sp.ID); err != nil || !ok || !slices.Equal(d.PRs, []string{"https://github.com/o/r/pull/7"}) {
		t.Fatalf("done = %+v, %v, %v", d, ok, err)
	}
	if q, ok, err := lb.LatestQueen(ctx, "queen-toy"); err != nil || !ok || q.Session != "default" || q.PID != 10 {
		t.Fatalf("queen = %+v, %v, %v", q, ok, err)
	}
	if in, err := lb.Sessions(ctx); err != nil || !slices.ContainsFunc(in, func(r logbook.Recorded) bool {
		return r.Name == "toy-1" && r.Session == "default" && r.Pane == "w3:p1" && r.Process.PID == 20
	}) {
		t.Fatalf("Sessions = %+v, %v", in, err)
	}
}

// bees-12.23 kept: a second Open finds the work done, and many concurrent Opens of an old
// logbook migrate it once, each seeing every row, leaving no temporary copies.
func TestMigrationRunsOnceUnderConcurrentOpens(t *testing.T) {
	for _, ddl := range []string{v0, v1} {
		path := write(t, ddl)
		var wg sync.WaitGroup
		errs := make([]error, 8)
		for i := range errs {
			wg.Go(func() {
				lb, err := logbook.Open(context.Background(), path)
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
		if n, v := shape(t, path); n != 4 || v != logbook.Version {
			t.Fatalf("after concurrent opens: %d rows, version %d", n, v)
		}
		if left, _ := filepath.Glob(path + ".pre-2.*"); len(left) != 0 {
			t.Fatalf("temporary copies left: %v", left)
		}
	}
}

// bees-12.23: concurrent first opens of a new logbook all succeed; SQLite can refuse the
// WAL switch with SQLITE_BUSY without waiting, so Open retries for a bounded time.
func TestConcurrentFirstOpensOfANewLogbookAllSucceed(t *testing.T) {
	for range 30 {
		path := filepath.Join(t.TempDir(), "bees.db")
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				lb, _, err := logbook.Create(context.Background(), path)
				if err != nil {
					t.Error(err)
					return
				}
				lb.Close()
			})
		}
		wg.Wait()
	}
}

// A new logbook starts at v2 with no copy aside.
func TestANewLogbookStartsAtTheCurrentVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bees.db")
	open(t, path)
	if _, err := os.Stat(path + ".pre-2"); !os.IsNotExist(err) {
		t.Fatalf("copy aside of a new logbook: %v", err)
	}
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != logbook.Version {
		t.Fatalf("version %d", v)
	}
}

// OpenReadOnly never migrates, creates or rebuilds: an unmigrated file stays at its
// version and still reads, and a missing one is an error.
func TestOpenReadOnlyNeverMigrates(t *testing.T) {
	ctx := context.Background()
	path := write(t, v0)
	lb, err := logbook.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	if v, err := lb.SchemaVersion(ctx); err != nil || v != 0 {
		t.Fatalf("version %d, %v", v, err)
	}
	if sp, ok, err := lb.LatestSpawn(ctx, "bees-8"); err != nil || !ok || !sp.Legacy() {
		t.Fatalf("read-only spawn: %+v, %v, %v", sp, ok, err)
	}
	if _, err := lb.Send(ctx, "a", "b", "c"); err == nil {
		t.Fatal("a read-only logbook took a write")
	}
	if _, err := os.Stat(path + ".pre-2"); !os.IsNotExist(err) {
		t.Fatal("OpenReadOnly copied aside")
	}
	missing := filepath.Join(t.TempDir(), "none.db")
	if _, err := logbook.OpenReadOnly(ctx, missing); err == nil {
		t.Fatal("OpenReadOnly of a missing file succeeded")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("OpenReadOnly created a file")
	}
}

// A spawn can't be recorded without a goal: it would read back as legacy.
func TestASpawnWithoutAGoalIsRefused(t *testing.T) {
	lb := open(t, filepath.Join(t.TempDir(), "bees.db"))
	if _, err := lb.RecordSpawn(context.Background(), "q", logbook.Spawn{Name: "w"}, ""); err != goal.ErrNoGoal {
		t.Fatalf("err = %v", err)
	}
}

// The old bs's open spawns have no start, so this bs can neither ring them nor see them
// run: Unstarted names them, on a file not yet migrated. The pilot's started one, and an
// ended one are not among them.
func TestUnstartedNamesTheOldBsOpenSpawns(t *testing.T) {
	ctx := context.Background()
	path := write(t, v0+`
INSERT INTO entries VALUES ('s2', '2026-10-01T00:00:04.000Z', 'spawned', 'queen-bees', 'bees-9', NULL, '{"bead":"bees-9","workspace":"w3","pane":"w3:p1","worktree":"/r/bees-WT/bees-9","branch":"bees-9","repo":"/r/bees"}');
INSERT INTO entries VALUES ('e2', '2026-10-01T00:00:05.000Z', 'ended', 'queen-bees', 'bees-9', 's2', NULL);`)
	lb, err := logbook.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	if got, err := lb.Unstarted(ctx); err != nil || !slices.Equal(got, []string{"bees-8"}) {
		t.Fatalf("v0: %v, %v", got, err)
	}
	if got, err := open(t, write(t, v1)).Unstarted(ctx); err != nil || len(got) != 0 {
		t.Fatalf("v1, started: %v, %v", got, err)
	}
}

// Open never makes a logbook: a missing file is ErrNoLogbook and stays missing, so a
// deleted logbook can't come back empty (R48, bees-8.12). Create makes it, once.
func TestOpenRefusesAMissingFileAndCreateMakesIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "bees.db")
	if _, err := logbook.Open(ctx, path); !errors.Is(err, logbook.ErrNoLogbook) {
		t.Fatalf("Open of a missing file: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("Open made its dir: %v", err)
	}
	lb, created, err := logbook.Create(ctx, path)
	if err != nil || !created {
		t.Fatalf("Create: %v %v", created, err)
	}
	lb.Close()
	lb, created, err = logbook.Create(ctx, path)
	if err != nil || created {
		t.Fatalf("Create of an existing file: %v %v", created, err)
	}
	lb.Close()
	if lb, err := logbook.Open(ctx, path); err != nil {
		t.Fatalf("Open once it exists: %v", err)
	} else {
		lb.Close()
	}
}

// A migration that finds a .pre-2 already there keeps it, and says so: that copy may be
// of another state than the one just migrated (bees-13.9 review SPEC6).
func TestAMigrationSaysWhenItKeptAnEarlierCopy(t *testing.T) {
	path := write(t, v0)
	if err := os.WriteFile(path+".pre-2", []byte("earlier"), 0o600); err != nil {
		t.Fatal(err)
	}
	lb := open(t, path)
	if lb.KeptCopy != path+".pre-2" {
		t.Fatalf("KeptCopy = %q", lb.KeptCopy)
	}
	if b, _ := os.ReadFile(path + ".pre-2"); string(b) != "earlier" {
		t.Fatalf("the earlier copy changed: %q", b)
	}
	if again := open(t, path); again.KeptCopy != "" {
		t.Fatalf("a logbook already migrated reports a kept copy: %q", again.KeptCopy)
	}
}

// A logbook opened AtFirstWrite reads empty and makes no file until its first write, which
// makes it once: of parallel first writers exactly one created it (bees-13.59, OQ-K1).
func TestALogbookIsMadeAtItsFirstWrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "bees.db")
	lb, err := logbook.AtFirstWrite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if sps, err := lb.LatestSpawns(ctx); err != nil || len(sps) != 0 {
		t.Fatalf("an unmade logbook reads %v, %v; want none", sps, err)
	}
	lb.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a logbook nothing wrote to was made: %v", err)
	}
	const n = 8
	created := make([]bool, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lb, err := logbook.AtFirstWrite(ctx, path)
			if err != nil {
				t.Error(err)
				return
			}
			defer lb.Close()
			if _, err := lb.Send(ctx, "keeper", "queen-toy", "hi"); err != nil {
				t.Error(err)
			}
			created[i] = lb.Created()
		}()
	}
	wg.Wait()
	if c := len(slices.DeleteFunc(created, func(b bool) bool { return !b })); c != 1 {
		t.Fatalf("%d writers made the logbook, want 1", c)
	}
	ro, err := logbook.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if ms, err := ro.Unread(ctx, "queen-toy"); err != nil || len(ms) != n {
		t.Fatalf("%d messages, %v; want %d", len(ms), err, n)
	}
}

// A file with no entries table (an older bs mid-creation) reads as an empty logbook.
func TestATablelessFileReadsEmpty(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bees.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lb, err := logbook.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	if sps, err := lb.LatestSpawns(ctx); err != nil || len(sps) != 0 {
		t.Fatalf("LatestSpawns = %v, %v", sps, err)
	}
}
