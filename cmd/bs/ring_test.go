package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/harness/claude/claudetest"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
)

// queenRunning records queen-test with this test's own process, as bs queen would.
func queenRunning(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	lb, err := logbook.Open(ctx, os.Getenv("BS_LOGBOOK"))
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	start, _ := proc.Start(os.Getpid())
	if _, _, err := lb.RecordQueen(ctx, logbook.Queen{Name: "queen-test", Harness: "claude_code", Pane: "w1:p1", PID: os.Getpid(), Start: start}, func(logbook.Queen) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestSendRingsTheRecipientWithItsUnreadCount(t *testing.T) {
	useTempLogbook(t)
	queenRunning(t)
	bs(t, "send", "--as", "bees-1-3", "queen-test", "first")
	reg := claudetest.FakeRegistry(t)
	got := reg.Add(t, "queen-test", "")
	bs(t, "send", "--as", "bees-1-2", "queen-test", "second")
	if _, stderr, code := bs(t, "send", "--as", "bees-1-2", "queen-test", "third"); code != 0 {
		t.Fatalf("send: exit %d: %s", code, stderr)
	}
	lines := got() // the fake listener takes only the first ring, sent after "second"
	msg, _ := lines[0]["message"].(map[string]any)
	want := `[bs] 2 unread (bees-1-3, bees-1-2) — run: bs inbox   (reply with: bs send bees-1-2 "...")`
	if msg["content"] != want || lines[0]["priority"] != "next" {
		t.Fatalf("doorbell = %v %q\nwant %q", lines[0]["priority"], msg["content"], want)
	}
}

func TestSendCountsRepeatSenders(t *testing.T) {
	useTempLogbook(t)
	started(t, "worker-a", true) // its start recorded this test's process, so bs rings it
	bs(t, "send", "--as", "queen", "worker-a", "one")
	got := claudetest.FakeRegistry(t).Add(t, "worker-a", "")
	bs(t, "send", "--as", "queen", "--now", "worker-a", "two")
	lines := got()
	msg, _ := lines[0]["message"].(map[string]any)
	want := `[bs] 2 unread (queen ×2) — run: bs inbox   (reply with: bs send queen "...")`
	if msg["content"] != want || lines[0]["priority"] != "now" {
		t.Fatalf("doorbell = %v %q\nwant now %q", lines[0]["priority"], msg["content"], want)
	}
}

func TestSendToAStoppedSessionQueuesTheMessage(t *testing.T) {
	useTempLogbook(t)
	endedWorker(t, "worker-a", t.TempDir())
	out, stderr, code := bs(t, "send", "--as", "queen", "worker-a", "hello")
	if code != 0 || !strings.Contains(out, "queued; worker-a is not running") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if inbox, _, _ := bs(t, "inbox", "--as", "worker-a"); !strings.Contains(inbox, "hello") {
		t.Fatalf("inbox = %q", inbox)
	}
}

func TestSendWarnsWhenTheRingFailsButKeepsTheMessage(t *testing.T) {
	useTempLogbook(t)
	started(t, "worker-a", true)
	reg := claudetest.FakeRegistry(t)
	reg.Add(t, "worker-a", "")
	reg.Edit(t, func(e map[string]any) { e["messagingSocketPath"] = "/nonexistent/s.sock" })
	_, stderr, code := bs(t, "send", "--as", "queen", "worker-a", "hello")
	if code != 0 || !strings.Contains(stderr, "couldn't ring worker-a") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if inbox, _, _ := bs(t, "inbox", "--as", "worker-a"); !strings.Contains(inbox, "hello") {
		t.Fatalf("inbox = %q", inbox)
	}
}

// A ring is a user turn, so bs rings only its project's queen and recorded workers;
// a message for anyone else is stored and never rung (bees-7.4).
func TestSendNeverRingsAStranger(t *testing.T) {
	useTempLogbook(t)
	reg := claudetest.FakeRegistry(t)
	reg.Add(t, "other-1", "")
	reg.Edit(t, func(e map[string]any) { e["messagingSocketPath"] = "/nonexistent/s.sock" }) // any ring would fail loudly
	out, stderr, code := bs(t, "send", "--as", "queen-test", "other-1", "hello")
	if code != 0 || strings.Contains(out, "queued") || strings.Contains(stderr, "couldn't ring") || !strings.Contains(stderr, "without ringing it") {
		t.Fatalf("exit %d, stdout %q, stderr %q; want stored, not rung", code, out, stderr)
	}
	if inbox, _, _ := bs(t, "inbox", "--as", "other-1"); !strings.Contains(inbox, "hello") {
		t.Fatalf("inbox = %q", inbox)
	}
}

// An agy worker has no ring: the message is stored and bs send says when it will be read.
func TestSendToAHarnessWithNoRingSaysSo(t *testing.T) {
	useTempLogbook(t)
	ctx := context.Background()
	lb, err := logbook.Open(ctx, os.Getenv("BS_LOGBOOK"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := lb.RecordSpawn(ctx, "queen-test", logbook.Spawn{Name: "worker-a", Bead: "worker-a", Harness: "agy"}, "")
	if err != nil {
		t.Fatal(err)
	}
	start, _ := proc.Start(os.Getpid())
	_, err = lb.RecordStart(ctx, logbook.Start{Spawn: id, Name: "worker-a", Process: proc.Process{PID: os.Getpid(), Start: start}})
	lb.Close()
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, code := bs(t, "send", "--as", "queen-test", "worker-a", "hello")
	if code != 0 || !strings.Contains(out, "stored; worker-a's harness has no ring yet") || stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
}

// bees-12.10 (T4): bs rings the process the recipient's entry recorded, never a session
// looked up by name: a registry entry called worker-a at another pid, or a recorded pid
// whose registry entry names someone else, is not rung.
func TestSendRingsOnlyTheRecordedProcess(t *testing.T) {
	useTempLogbook(t)
	started(t, "worker-a", false) // its recorded process is gone (a reused pid)
	reg := claudetest.FakeRegistry(t)
	reg.Add(t, "worker-a", "")
	reg.Edit(t, func(e map[string]any) { e["messagingSocketPath"] = "/nonexistent/s.sock" }) // any ring would fail loudly
	out, stderr, code := bs(t, "send", "--as", "queen-test", "worker-a", "hello")
	if code != 0 || !strings.Contains(out, "queued; worker-a is not running") || strings.Contains(stderr, "couldn't ring") {
		t.Fatalf("exit %d, stdout %q, stderr %q; want queued, never rung by name", code, out, stderr)
	}
	started(t, "worker-b", true) // alive at this pid, but the registry there says worker-a
	_, stderr, code = bs(t, "send", "--as", "queen-test", "worker-b", "hello")
	if code != 0 || !strings.Contains(stderr, `is "worker-a", not worker-b`) {
		t.Fatalf("exit %d, stderr %q; want the ring refused", code, stderr)
	}
}

// bees-12.10 (T4): a queen whose recorded process is gone, or a queen with no queen entry
// (started by a bs from before queen entries), is not rung, even with a registry entry by
// her name: the message is queued.
func TestSendToAGoneQueenQueues(t *testing.T) {
	useTempLogbook(t)
	reg := claudetest.FakeRegistry(t)
	reg.Add(t, "queen-test", "")
	reg.Edit(t, func(e map[string]any) { e["messagingSocketPath"] = "/nonexistent/s.sock" }) // any ring would fail loudly
	if out, stderr, code := bs(t, "send", "--as", "bees-1-2", "queen-test", "hi"); code != 0 || !strings.Contains(out, "queued; queen-test is not running") {
		t.Fatalf("no queen entry: exit %d, %q, %q", code, out, stderr)
	}
	ctx := context.Background()
	lb, err := logbook.Open(ctx, os.Getenv("BS_LOGBOOK"))
	if err != nil {
		t.Fatal(err)
	}
	start, _ := proc.Start(os.Getpid())
	_, _, err = lb.RecordQueen(ctx, logbook.Queen{Name: "queen-test", PID: os.Getpid(), Start: start + "0"}, func(logbook.Queen) (bool, error) { return false, nil })
	lb.Close()
	if err != nil {
		t.Fatal(err)
	}
	if out, stderr, code := bs(t, "send", "--as", "bees-1-2", "queen-test", "hi"); code != 0 || !strings.Contains(out, "queued; queen-test is not running") {
		t.Fatalf("reused pid: exit %d, %q, %q", code, out, stderr)
	}
}
