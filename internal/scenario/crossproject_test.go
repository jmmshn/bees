package scenario_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/fake"
	"github.com/jmmshn/bees/internal/logbook"
)

// otherProject makes project other beside the toy: a repo and an empty logbook, no queen.
func (x *toy) otherProject() (repo, db string) {
	x.t.Helper()
	repo = filepath.Join(filepath.Dir(x.repo), "other")
	x.git(filepath.Dir(x.repo), "init", "-q", "-b", "main", repo)
	x.commit(repo, "README")
	db = filepath.Join(filepath.Dir(x.logbook), "other.db")
	lb, _, err := logbook.Create(context.Background(), db)
	if err != nil {
		x.t.Fatal(err)
	}
	lb.Close()
	return repo, db
}

// T2 (S2, motif 7): two sends into a project whose queen has never run race to start her;
// the twin lock lets one start her, on the herdr session of the config in effect, and the
// other waits and rings her: one queen entry, both messages in her inbox.
func TestCrossProjectSendsRaceToOneQueen(t *testing.T) {
	x := newToy(t)
	ctx := context.Background()
	repo, db := x.otherProject()
	x.spawn("toy-1", "verdict")
	x.spawn("toy-2", "verdict")
	ws := []string{"toy-1", "toy-2"}
	for _, w := range ws {
		x.harness.Step(t, w, "cd", repo)
		x.harness.Await(t, w, 1)
	}
	for _, w := range ws {
		x.harness.Step(t, w, "bs", "send", "queen-other", "racing "+w)
	}
	started := 0
	for _, w := range ws {
		r := x.harness.Await(t, w, 2)
		if r.Code != 0 || !strings.Contains(r.Stdout, "rung queen-other") {
			t.Fatalf("%s's send: exit %d:\n%s%s", w, r.Code, r.Stdout, r.Stderr)
		}
		started += strings.Count(r.Stdout, "started queen-other in pane ")
	}
	if started != 1 {
		t.Fatalf("%d sends started queen-other, want 1", started)
	}
	ro, err := logbook.OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	q, ok, err := ro.LatestQueen(ctx, "queen-other")
	if err != nil || !ok || q.Session != "toy" || !sameDir(q.Repo, repo) {
		t.Fatalf("queen-other's entry: %+v %v %v", q, ok, err)
	}
	if alive, _ := x.herdr.Alive(q.Process); !alive {
		t.Fatal("queen-other isn't running")
	}
	if !strings.HasSuffix(q.Exe, "/claude") {
		t.Fatalf("her harness: %s", q.Exe)
	}
	ms, err := ro.Unread(ctx, "queen-other")
	if err != nil || len(ms) != 2 {
		t.Fatalf("her inbox: %+v %v", ms, err)
	}
	for _, m := range ms {
		if m.FromProject != "toy" {
			t.Fatalf("a message without its sender's project: %+v", m)
		}
	}
	if n := x.sqlOneAt(db, `SELECT count(*) FROM entries WHERE kind = 'queen'`); n != "1" {
		t.Fatalf("%s queen entries, want 1", n)
	}
}

// T2 (R4, OQ-K7): the auto-start runs the shared bs on PATH, never a worker's build: a bs
// in worker.path's dirs or the sender's own workspace is skipped, and with none left the
// message is stored, not rung, and bs says how to start her by hand.
func TestCrossProjectStartSkipsAWorkersBuild(t *testing.T) {
	x := newToy(t)
	repo, _ := x.otherProject()
	bin := t.TempDir()
	if err := os.MkdirAll(filepath.Join(x.config, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	x.write(filepath.Join(x.config, "projects", "other.toml"), "[worker]\npath = [\""+bin+"\"]\n")
	sp := x.spawn("toy-1", "verdict")
	own := filepath.Join(sp.Workspace.Dir, "bin")
	for _, d := range []string{bin, own} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(filepath.Dir(fake.Beesfake(t)), "bs"), filepath.Join(d, "bs")); err != nil {
			t.Fatal(err)
		}
	}
	for i, step := range [][]string{{"cd", repo}, {"env", "PATH=" + own + string(filepath.ListSeparator) + bin}} {
		x.harness.Step(t, "toy-1", step...)
		x.harness.Await(t, "toy-1", i+1)
	}
	must(t, x.worker("toy-1", 3, "send", "queen-other", "hi"), 0,
		"queen-other isn't running and wasn't started: no bs on PATH outside worker.path and the sender's workspace",
		"next: bs queen, in a herdr pane in other's repo",
		"stored, not rung (queen-other: not running)")
}

// sqlOneAt is the first column of query's first row in the logbook at path.
func (x *toy) sqlOneAt(path, query string) string {
	y := *x
	y.logbook = path
	return y.sqlOne(query)
}

// sameDir reports whether a and b name one directory through symlinks (macOS's /var is
// /private/var).
func sameDir(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
