package main

import (
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/harness/claude/claudetest"
)

func TestSendRingsTheRecipientWithItsUnreadCount(t *testing.T) {
	useTempLogbook(t)
	bs(t, "send", "--as", "bees-1-3", "queen", "first")
	reg := claudetest.FakeRegistry(t)
	got := reg.Add(t, "queen", "")
	bs(t, "send", "--as", "bees-1-2", "queen", "second")
	if _, stderr, code := bs(t, "send", "--as", "bees-1-2", "queen", "third"); code != 0 {
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
	bs(t, "send", "--as", "queen", "bee-a", "one")
	got := claudetest.FakeRegistry(t).Add(t, "bee-a", "")
	bs(t, "send", "--as", "queen", "--now", "bee-a", "two")
	lines := got()
	msg, _ := lines[0]["message"].(map[string]any)
	want := `[bs] 2 unread (queen ×2) — run: bs inbox   (reply with: bs send queen "...")`
	if msg["content"] != want || lines[0]["priority"] != "now" {
		t.Fatalf("doorbell = %v %q\nwant now %q", lines[0]["priority"], msg["content"], want)
	}
}

func TestSendToAStoppedSessionQueuesTheMessage(t *testing.T) {
	useTempLogbook(t)
	out, stderr, code := bs(t, "send", "--as", "queen", "bee-a", "hello")
	if code != 0 || !strings.Contains(out, "queued; bee-a is not running") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if inbox, _, _ := bs(t, "inbox", "--as", "bee-a"); !strings.Contains(inbox, "hello") {
		t.Fatalf("inbox = %q", inbox)
	}
}

func TestSendWarnsWhenTheRingFailsButKeepsTheMessage(t *testing.T) {
	useTempLogbook(t)
	reg := claudetest.FakeRegistry(t)
	reg.Add(t, "bee-a", "")
	reg.Edit(t, func(e map[string]any) { e["messagingSocketPath"] = "/nonexistent/s.sock" })
	_, stderr, code := bs(t, "send", "--as", "queen", "bee-a", "hello")
	if code != 0 || !strings.Contains(stderr, "couldn't ring bee-a") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if inbox, _, _ := bs(t, "inbox", "--as", "bee-a"); !strings.Contains(inbox, "hello") {
		t.Fatalf("inbox = %q", inbox)
	}
}
