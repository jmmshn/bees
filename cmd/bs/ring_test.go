package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/harness/claude/claudetest"
	"github.com/jmmshn/bees/internal/logbook"
)

func TestSendRingsTheRecipientWithItsUnreadCount(t *testing.T) {
	useTempLogbook(t)
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
	endedWorker(t, "worker-a", t.TempDir()) // recorded in the project, so bs rings it
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
	endedWorker(t, "worker-a", t.TempDir())
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
	_, err = lb.RecordSpawn(ctx, "queen-test", logbook.Spawn{Name: "worker-a", Bead: "worker-a", Harness: "agy"}, "")
	lb.Close()
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, code := bs(t, "send", "--as", "queen-test", "worker-a", "hello")
	if code != 0 || !strings.Contains(out, "stored; worker-a's harness has no ring yet") || stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
}
