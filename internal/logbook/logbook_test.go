package logbook_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jmmshn/bees/internal/logbook"
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
	if _, err := lb.Send(ctx, "queen", "bee-a", "add tests for parser"); err != nil {
		t.Fatal(err)
	}
	got, err := lb.Unread(ctx, "bee-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].From != "queen" || got[0].To != "bee-a" || got[0].Body != "add tests for parser" {
		t.Fatalf("unread for bee-a = %+v", got)
	}
	other, err := lb.Unread(ctx, "bee-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("unread for bee-b = %+v, want none", other)
	}
}

func TestRecordReceiptsRemovesFromUnreadAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	first, err := lb.Send(ctx, "queen", "bee-a", "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lb.Send(ctx, "queen", "bee-a", "two"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := lb.RecordReceipts(ctx, "bee-a", []logbook.ID{first}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := lb.Unread(ctx, "bee-a")
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
	id, err := lb.Send(ctx, "queen", "bee-a", "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := lb.RecordReceipts(ctx, "bee-b", []logbook.ID{id}); err != nil {
		t.Fatal(err)
	}
	got, err := lb.Unread(ctx, "bee-a")
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
	if _, err := lb.Send(ctx, "queen", "bee-a", "older"); err != nil {
		t.Fatal(err)
	}
	newer, err := lb.Send(ctx, "bee-b", "bee-a", "newer")
	if err != nil {
		t.Fatal(err)
	}
	if err := lb.RecordReceipts(ctx, "bee-a", []logbook.ID{newer}); err != nil {
		t.Fatal(err)
	}
	got, err := lb.Unread(ctx, "bee-a")
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
			lb, err := logbook.Open(ctx, path) // own handle, like a separate bs process
			if err != nil {
				errs <- err
				return
			}
			defer lb.Close()
			_, err = lb.Send(ctx, fmt.Sprintf("bee-%d", i), "queen", "done")
			if err == nil {
				err = lb.RecordReceipts(ctx, fmt.Sprintf("bee-%d", i), nil)
			}
			errs <- err
		}()
	}
	for range senders {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	got, err := setup.Unread(ctx, "queen")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != senders {
		t.Fatalf("queen has %d unread, want %d", len(got), senders)
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
	if _, err := lb.Send(ctx, "queen", "bee-a", "hi"); err != nil {
		t.Fatalf("send without init: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("logbook not at the given path: %v", err)
	}
}

func TestSpawnRecordsTrackTheLatestSpawnAndItsEnd(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	if _, ok, err := lb.LatestSpawn(ctx, "bee-995-8"); err != nil || ok {
		t.Fatalf("before any spawn: ok=%v err=%v", ok, err)
	}
	first := logbook.Spawn{Name: "bee-995-8", Bead: "beads-995.8", Workspace: "w7", Pane: "w7:p1", Worktree: "/r-WT/bee-995-8", Branch: "bee-995-8"}
	id, err := lb.RecordSpawn(ctx, "queen-bees", first, "your brief")
	if err != nil {
		t.Fatal(err)
	}
	if unread, _ := lb.Unread(ctx, "bee-995-8"); len(unread) != 1 || unread[0].Body != "your brief" {
		t.Fatalf("brief = %+v", unread)
	}
	got, ok, err := lb.LatestSpawn(ctx, "bee-995-8")
	if err != nil || !ok || got.ID != id || got.Workspace != "w7" || got.Bead != "beads-995.8" || got.Ended {
		t.Fatalf("latest = %+v ok=%v err=%v", got, ok, err)
	}
	for range 2 { // ending twice is a no-op
		if err := lb.RecordEnd(ctx, "queen-bees", id); err != nil {
			t.Fatal(err)
		}
	}
	if got, _, _ = lb.LatestSpawn(ctx, "bee-995-8"); !got.Ended {
		t.Fatalf("after end: %+v, want Ended", got)
	}
	second := first
	second.Workspace = "w9"
	if _, err := lb.RecordSpawn(ctx, "queen-bees", second, ""); err != nil {
		t.Fatal(err)
	}
	if got, _, _ = lb.LatestSpawn(ctx, "bee-995-8"); got.Workspace != "w9" || got.Ended {
		t.Fatalf("respawn: %+v, want open spawn in w9", got)
	}
}

func TestEndingASpawnRetiresItsUnreadBrief(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	// A spawn whose start failed: its brief was never read (bees-1.14).
	id, _ := lb.RecordSpawn(ctx, "queen", logbook.Spawn{Name: "bee-a", Bead: "p-2", Workspace: "w1"}, "old brief")
	lb.Send(ctx, "queen", "bee-a", "a message, not a brief")
	if err := lb.RecordEnd(ctx, "queen", id); err != nil {
		t.Fatal(err)
	}
	lb.RecordSpawn(ctx, "queen", logbook.Spawn{Name: "bee-a", Bead: "p-2", Workspace: "w2"}, "new brief")
	unread, err := lb.Unread(ctx, "bee-a")
	if err != nil || len(unread) != 2 || unread[0].Body != "a message, not a brief" || unread[1].Body != "new brief" {
		t.Fatalf("unread = %+v, %v; want the message and the new brief only", unread, err)
	}
	if got, _ := lb.UnreadCounts(ctx); got["bee-a"] != 2 {
		t.Fatalf("UnreadCounts = %v, want bee-a: 2", got)
	}
}

func TestLatestSpawnKnowsWhetherTheBeeHasReadSinceIt(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	sp := logbook.Spawn{Name: "bee-a", Bead: "p-2", Workspace: "w1"}
	first, _ := lb.RecordSpawn(ctx, "queen", sp, "brief")
	if got, _, _ := lb.LatestSpawn(ctx, "bee-a"); got.Read {
		t.Fatalf("before any inbox: %+v, want not Read", got)
	}
	unread, _ := lb.Unread(ctx, "bee-a")
	lb.RecordReceipts(ctx, "bee-a", []logbook.ID{unread[0].ID})
	if got, _, _ := lb.LatestSpawn(ctx, "bee-a"); !got.Read {
		t.Fatalf("after its inbox: %+v, want Read", got)
	}
	lb.RecordEnd(ctx, "queen", first)
	lb.RecordSpawn(ctx, "queen", sp, "brief")
	if got, _, _ := lb.LatestSpawn(ctx, "bee-a"); got.Read {
		t.Fatalf("respawned: %+v, want not Read (an earlier spawn's reads don't count)", got)
	}
}

func TestUnreadCountsNamesEverySessionWithItsUnread(t *testing.T) {
	ctx := context.Background()
	lb := open(t)
	lb.Send(ctx, "queen", "bee-a", "one")
	lb.Send(ctx, "queen", "bee-a", "two")
	id, _ := lb.Send(ctx, "bee-a", "queen", "done")
	lb.RecordReceipts(ctx, "queen", []logbook.ID{id})
	lb.RecordSpawn(ctx, "queen", logbook.Spawn{Name: "bee-b", Bead: "p-2", Workspace: "w1"}, "brief")
	got, err := lb.UnreadCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"queen": 0, "bee-a": 2, "bee-b": 1} // bee-b's brief is unread
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Counts = %v, want %v", got, want)
	}
}

// TestKilledWriterLosesNoCommittedMessage SIGKILLs a separate process mid-way through a
// stream of sends. Every send it acknowledged must survive, and the logbook must still
// read and write.
func TestKilledWriterLosesNoCommittedMessage(t *testing.T) {
	if path := os.Getenv("BS_TEST_WRITER"); path != "" {
		writeForever(path) // the child: never returns
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logbook.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestKilledWriterLosesNoCommittedMessage$")
	cmd.Env = append(os.Environ(), "BS_TEST_WRITER="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	acked := map[logbook.ID]bool{}
	sc := bufio.NewScanner(out)
	for len(acked) < 200 && sc.Scan() {
		acked[logbook.ID(sc.Text())] = true
	}
	cmd.Process.Signal(syscall.SIGKILL) // mid-write: the child is still sending
	cmd.Wait()
	if len(acked) < 200 {
		t.Fatalf("writer acknowledged only %d sends", len(acked))
	}

	lb, err := logbook.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	got, err := lb.Unread(ctx, "queen")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		delete(acked, m.ID)
	}
	if len(acked) != 0 {
		t.Fatalf("%d acknowledged sends lost", len(acked))
	}
	if _, err := lb.Send(ctx, "bee-b", "queen", "after the crash"); err != nil {
		t.Fatalf("send after the crash: %v", err)
	}
}

// writeForever sends to queen in a loop, printing each id once Send has returned.
func writeForever(path string) {
	ctx := context.Background()
	lb, err := logbook.Open(ctx, path)
	if err != nil {
		os.Exit(1)
	}
	for {
		id, err := lb.Send(ctx, "bee-a", "queen", "work")
		if err != nil {
			os.Exit(1)
		}
		fmt.Println(id)
	}
}
