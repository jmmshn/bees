package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
)

// inWorkerPane records worker name with this test's process, and puts this test in its
// pane: its bs calls are the worker's.
func inWorkerPane(t *testing.T, name string) {
	t.Helper()
	t.Setenv("BS_LOGBOOK", "")
	started(t, name, true)
	lb, err := logbook.Open(context.Background(), fleetLogbook(t))
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	sp, _, _ := lb.LatestSpawn(context.Background(), name)
	t.Setenv("HERDR_PANE_ID", sp.Pane)
}

// bees-12.18: bs done is a done entry for the queen, unread in her inbox and shown by bs
// status; it needs a recorded worker pane, exactly one of --pr and --report, and only the
// pane's own project.
func TestDoneReportsToTheQueen(t *testing.T) {
	useTempLogbook(t)
	t.Setenv("BS_SESSION", "test-1-1")
	if _, stderr, code := bs(t, "done", "--pr", "https://x/pull/7"); code != 2 || !strings.Contains(stderr, "runs only inside a worker's session") {
		t.Fatalf("outside a pane: exit %d, %q; want 2", code, stderr)
	}
	t.Setenv("BS_SESSION", "")
	inWorkerPane(t, "test-1-1")
	for _, args := range [][]string{{"done"}, {"done", "--pr", "u", "--report", "r"}, {"done", "--pr", "u", "text"}, {"done", "a", "b"}} {
		if _, _, code := bs(t, args...); code != 2 {
			t.Fatalf("%v: exit %d, want usage", args, code)
		}
	}
	if _, stderr, code := bs(t, "done", "--project", "dotfiles", "--pr", "u"); code == 0 || !strings.Contains(stderr, "reports done only in its own project") {
		t.Fatalf("--project dotfiles: exit %d, %q", code, stderr)
	}
	out, stderr, code := bs(t, "done", "--pr", "https://x/pull/7")
	if code != 0 || !strings.Contains(out, "test-1-1 recorded for queen-test") {
		t.Fatalf("done: exit %d, %q, %q", code, out, stderr)
	}
	t.Setenv("HERDR_PANE_ID", "")
	if out, _, _ := bs(t, "status"); !strings.Contains(out, "https://x/pull/7") {
		t.Fatalf("status = %s, want the done's PR", out)
	}
	if out, _, _ := bs(t, "inbox", "--as", "queen-test"); !strings.Contains(out, "from test-1-1") || !strings.Contains(out, "done: test-1-1 PR https://x/pull/7") {
		t.Fatalf("queen's inbox = %q", out)
	}
}

// bees-12.18 (T10, live process table): a child this worker started after its start
// refuses bs done with exit 5, naming it; --force records it in the entry.
func TestDoneRefusesBackgroundWork(t *testing.T) {
	useTempLogbook(t)
	inWorkerPane(t, "test-1-1")
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	_, stderr, code := bs(t, "done", "--pr", "https://x/pull/7")
	if code != exitBackground || !strings.Contains(stderr, "still runs background work") || !strings.Contains(stderr, "sleep 30") {
		t.Fatalf("exit %d, %q; want 5 naming sleep 30", code, stderr)
	}
	out, stderr, code := bsIn(t, "left the docs server up\n", "done", "--force", "--report", "-")
	if code != 0 {
		t.Fatalf("--force: exit %d, %q, %q", code, out, stderr)
	}
	t.Setenv("HERDR_PANE_ID", "")
	if out, _, _ := bs(t, "inbox", "--as", "queen-test"); !strings.Contains(out, "left the docs server up") || !strings.Contains(out, "bs done --force") || !strings.Contains(out, "sleep 30") {
		t.Fatalf("queen's inbox = %q, want the forced done listing sleep 30", out)
	}
}

// bees-12.18: a start whose baseline couldn't be read can't be gated: bs done says so with
// exit 6, and --force reports anyway, telling the queen it went unchecked.
func TestDoneWithAnUncheckableStart(t *testing.T) {
	useTempLogbook(t)
	t.Setenv("BS_LOGBOOK", "")
	ctx := context.Background()
	lb, err := logbook.Open(ctx, fleetLogbook(t))
	if err != nil {
		t.Fatal(err)
	}
	id, err := lb.RecordSpawn(ctx, "queen-test", logbook.Spawn{Name: "test-1-2", Bead: "test-1.2", Pane: "w9:p1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	start, _ := proc.Start(os.Getpid())
	if _, err := lb.RecordStart(ctx, logbook.Start{Spawn: id, Name: "test-1-2", Process: proc.Process{PID: os.Getpid(), Start: start}, BaselineErr: "read /proc: too many open files"}); err != nil {
		t.Fatal(err)
	}
	lb.Close()
	t.Setenv("HERDR_PANE_ID", "w9:p1")
	if _, stderr, code := bs(t, "done", "--pr", "u"); code != exitCantCheck || !strings.Contains(stderr, "bs can't check for background work") {
		t.Fatalf("exit %d, %q; want 6", code, stderr)
	}
	if _, stderr, code := bs(t, "done", "--force", "--pr", "u"); code != 0 {
		t.Fatalf("--force: exit %d, %q", code, stderr)
	}
	t.Setenv("HERDR_PANE_ID", "")
	if out, _, _ := bs(t, "inbox", "--as", "queen-test"); !strings.Contains(out, "(reported with bs done --force; bs can't check for background work") {
		t.Fatalf("queen's inbox = %q", out)
	}
}

// bees-12.51: workers write bs done "<text>", so one bare argument is the report.
func TestDoneTakesABareReport(t *testing.T) {
	useTempLogbook(t)
	inWorkerPane(t, "test-1-1")
	if out, stderr, code := bs(t, "done", "pushed branch test-1-1 at abc123"); code != 0 || !strings.Contains(out, "test-1-1 recorded for queen-test") {
		t.Fatalf("bare report: exit %d, %q, %q", code, out, stderr)
	}
	t.Setenv("HERDR_PANE_ID", "")
	if out, _, _ := bs(t, "inbox", "--as", "queen-test"); !strings.Contains(out, "pushed branch test-1-1 at abc123") {
		t.Fatalf("queen's inbox = %q, want the report", out)
	}
}
