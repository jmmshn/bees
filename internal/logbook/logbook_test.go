package logbook_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmmshn/peons/internal/logbook"
)

func open(t *testing.T) *logbook.Logbook {
	t.Helper()
	ctx := context.Background()
	lb, err := logbook.Open(ctx, filepath.Join(t.TempDir(), "sub", "logbook.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lb.Close() })
	return lb
}

func TestSentMessageIsUnreadForRecipientOnly(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	if _, err := lb.Send(ctx, "chief", "peon-a", "add tests for parser"); err != nil {
		t.Fatal(err)
	}
	got, err := lb.Unread(ctx, "peon-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].From != "chief" || got[0].To != "peon-a" || got[0].Body != "add tests for parser" {
		t.Fatalf("unread for peon-a = %+v", got)
	}
	other, err := lb.Unread(ctx, "peon-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("unread for peon-b = %+v, want none", other)
	}
}

func TestRecordReceiptsRemovesFromUnreadAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	first, err := lb.Send(ctx, "chief", "peon-a", "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lb.Send(ctx, "chief", "peon-a", "two"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := lb.RecordReceipts(ctx, "peon-a", []logbook.ID{first}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := lb.Unread(ctx, "peon-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "two" {
		t.Fatalf("unread = %+v, want only \"two\"", got)
	}
}

func TestRecordReceiptsByAnotherSessionDoesNotCount(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	id, err := lb.Send(ctx, "chief", "peon-a", "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := lb.RecordReceipts(ctx, "peon-b", []logbook.ID{id}); err != nil {
		t.Fatal(err)
	}
	got, err := lb.Unread(ctx, "peon-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("unread = %+v, want the message still unread", got)
	}
}

func TestReadingANewerMessageLeavesOlderOnesUnread(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	if _, err := lb.Send(ctx, "chief", "peon-a", "older"); err != nil {
		t.Fatal(err)
	}
	newer, err := lb.Send(ctx, "peon-b", "peon-a", "newer")
	if err != nil {
		t.Fatal(err)
	}
	if err := lb.RecordReceipts(ctx, "peon-a", []logbook.ID{newer}); err != nil {
		t.Fatal(err)
	}
	got, err := lb.Unread(ctx, "peon-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "older" {
		t.Fatalf("unread = %+v, want only \"older\"", got)
	}
}

func TestConcurrentSendersFromSeparateProcessesLoseNothing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logbook.db")
	setup, err := logbook.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()

	const senders = 20
	errs := make(chan error, senders)
	for i := range senders {
		go func() {
			lb, err := logbook.Open(ctx, path) // own handle, like a separate pns process
			if err != nil {
				errs <- err
				return
			}
			defer lb.Close()
			_, err = lb.Send(ctx, fmt.Sprintf("peon-%d", i), "chief", "done")
			if err == nil {
				err = lb.RecordReceipts(ctx, fmt.Sprintf("peon-%d", i), nil)
			}
			errs <- err
		}()
	}
	for range senders {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	got, err := setup.Unread(ctx, "chief")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != senders {
		t.Fatalf("chief has %d unread, want %d", len(got), senders)
	}
}

func TestOpenHandlesOddPathsAndCreatesSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "we?ird#dir", "logbook.db")
	lb, err := logbook.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	if _, err := lb.Send(ctx, "chief", "peon-a", "hi"); err != nil {
		t.Fatalf("send without init: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("logbook not at the given path: %v", err)
	}
}

func TestSpawnRecordsTrackTheLatestSpawnAndItsEnd(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	if _, ok, err := lb.LatestSpawn(ctx, "peon-995-8"); err != nil || ok {
		t.Fatalf("before any spawn: ok=%v err=%v", ok, err)
	}
	first := logbook.Spawn{Name: "peon-995-8", Bead: "beads-995.8", Workspace: "w7", Pane: "w7:p1", Worktree: "/r-WT/peon-995-8", Branch: "peon-995-8"}
	id, err := lb.RecordSpawn(ctx, "chief-peons", first, "your brief")
	if err != nil {
		t.Fatal(err)
	}
	if unread, _ := lb.Unread(ctx, "peon-995-8"); len(unread) != 1 || unread[0].Body != "your brief" {
		t.Fatalf("brief = %+v", unread)
	}
	got, ok, err := lb.LatestSpawn(ctx, "peon-995-8")
	if err != nil || !ok || got.ID != id || got.Workspace != "w7" || got.Bead != "beads-995.8" || got.Ended {
		t.Fatalf("latest = %+v ok=%v err=%v", got, ok, err)
	}
	for range 2 { // ending twice is a no-op
		if err := lb.RecordEnd(ctx, "chief-peons", id); err != nil {
			t.Fatal(err)
		}
	}
	if got, _, _ = lb.LatestSpawn(ctx, "peon-995-8"); !got.Ended {
		t.Fatalf("after end: %+v, want Ended", got)
	}
	second := first
	second.Workspace = "w9"
	if _, err := lb.RecordSpawn(ctx, "chief-peons", second, ""); err != nil {
		t.Fatal(err)
	}
	if got, _, _ = lb.LatestSpawn(ctx, "peon-995-8"); got.Workspace != "w9" || got.Ended {
		t.Fatalf("respawn: %+v, want open spawn in w9", got)
	}
}

func TestCountsNamesEverySessionWithItsUnread(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	lb.Send(ctx, "chief", "peon-a", "one")
	lb.Send(ctx, "chief", "peon-a", "two")
	id, _ := lb.Send(ctx, "peon-a", "chief", "done")
	lb.RecordReceipts(ctx, "chief", []logbook.ID{id})
	lb.RecordSpawn(ctx, "chief", logbook.Spawn{Name: "peon-b", Bead: "p-2", Workspace: "w1"}, "brief")
	got, err := lb.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"chief": 0, "peon-a": 2, "peon-b": 1} // peon-b's brief is unread
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Counts = %v, want %v", got, want)
	}
}
