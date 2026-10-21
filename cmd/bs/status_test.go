package main

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
)

// started records a spawn of worker name and a start whose process is this test's own
// (running) or a reused pid (not running).
func started(t *testing.T, name string, running bool) {
	t.Helper()
	ctx := context.Background()
	lb, err := logbook.Open(ctx, cmp.Or(os.Getenv("BS_LOGBOOK"), fleetLogbook(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	id, err := lb.RecordSpawn(ctx, "queen", logbook.Spawn{Name: name, Bead: name, Pane: "w-" + name + ":p1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	start, _ := proc.Start(os.Getpid())
	if !running {
		start += "0"
	}
	if _, err := lb.RecordStart(ctx, logbook.Start{Spawn: id, Name: name, Process: proc.Process{PID: os.Getpid(), Start: start}}); err != nil {
		t.Fatal(err)
	}
}

// bees-12.10: a session runs while the process its entry recorded is alive.
func TestStatusShowsRunningAndUnreadForEachSession(t *testing.T) {
	useTempLogbook(t)
	started(t, "worker-a", true)
	started(t, "worker-b", false)
	bs(t, "send", "--as", "queen", "worker-a", "one")
	bs(t, "send", "--as", "queen", "worker-a", "two")
	bs(t, "send", "--as", "queen", "worker-b", "three")
	out, stderr, code := bs(t, "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	want := "SESSION   LABEL  STATE        UNREAD\n" + // queen: stopped with nothing unread, so left out
		"worker-a  -      running      2\n" +
		"worker-b  -      not running  1\n"
	if out != want {
		t.Fatalf("status =\n%s\nwant\n%s", out, want)
	}
}

func TestStatusShowsAWorkersLabel(t *testing.T) {
	useTempLogbook(t)
	ctx := context.Background()
	lb, err := logbook.Open(ctx, os.Getenv("BS_LOGBOOK"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = lb.RecordSpawn(ctx, "queen-test", logbook.Spawn{Name: "test-3", Bead: "test-3", Label: "role prompts"}, "brief")
	lb.Close()
	if err != nil {
		t.Fatal(err)
	}
	out, _, _ := bs(t, "status")
	if !strings.Contains(out, "test-3   role prompts  not running  1\n") {
		t.Fatalf("status =\n%s", out)
	}
	if out, _, _ := bs(t, "status", "--json"); !strings.Contains(out, `"label":"role prompts"`) {
		t.Fatalf("status --json = %s", out)
	}
}

func TestStatusJSON(t *testing.T) {
	useTempLogbook(t)
	bs(t, "send", "--as", "queen", "worker-a", "one")
	ctx := context.Background()
	lb, err := logbook.Open(ctx, os.Getenv("BS_LOGBOOK"))
	if err != nil {
		t.Fatal(err)
	}
	start, _ := proc.Start(os.Getpid())
	_, _, err = lb.RecordQueen(ctx, logbook.Queen{Name: "queen-test", PID: os.Getpid(), Start: start}, func(logbook.Queen) (bool, error) { return false, nil })
	lb.Close()
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := bs(t, "status", "--json")
	var got []struct {
		Session string `json:"session"`
		Running bool   `json:"running"`
		Unread  int    `json:"unread"`
	}
	if err := json.Unmarshal([]byte(out), &got); code != 0 || err != nil {
		t.Fatalf("exit %d, %v: %q", code, err, out)
	}
	if len(got) != 2 || got[0].Session != "queen-test" || !got[0].Running || got[0].Unread != 0 ||
		got[1].Session != "worker-a" || got[1].Running || got[1].Unread != 1 {
		t.Fatalf("status --json = %+v", got)
	}
}

func TestStatusRecordsNoReceipts(t *testing.T) {
	useTempLogbook(t)
	bs(t, "send", "--as", "queen", "worker-a", "hello")
	bs(t, "status")
	if out, _, _ := bs(t, "inbox", "--as", "worker-a"); !strings.Contains(out, "hello") {
		t.Fatalf("inbox after status = %q", out)
	}
}

func TestStatusWithAnEmptyLogbook(t *testing.T) {
	useTempLogbook(t)
	if out, _, code := bs(t, "status"); code != 0 || out != "no running sessions and no unread messages\n" {
		t.Fatalf("exit %d, %q", code, out)
	}
}

// bees-12.10 (T5): bs status reads liveness from the recorded processes, never from
// herdr, so it works while herdr is down. A worker with no recorded start (spawned before
// bs recorded starts) shows as not running, and bs says so.
func TestStatusWorksWithHerdrDown(t *testing.T) {
	useTempLogbook(t)
	h := useFakeHive(t)
	h.down = true
	started(t, "worker-a", true)
	ctx := context.Background()
	lb, err := logbook.Open(ctx, os.Getenv("BS_LOGBOOK"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = lb.RecordSpawn(ctx, "queen", logbook.Spawn{Name: "worker-b", Bead: "worker-b", Pane: "w9:p1"}, "")
	lb.Close()
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, code := bs(t, "status")
	if code != 0 || !strings.Contains(out, "worker-a  -      running") || !strings.Contains(stderr, "no process recorded for worker-b") {
		t.Fatalf("exit %d, %q, %q; want worker-a running and a note on worker-b", code, out, stderr)
	}
}
