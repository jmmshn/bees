package main

import (
	"strings"
	"testing"

	"github.com/jmmshn/peons/internal/harness/claude/claudetest"
)

func TestSendRingsTheRecipientWithItsUnreadCount(t *testing.T) {
	useTempLogbook(t)
	pns(t, "send", "--as", "peons-1-3", "chief", "first")
	reg := claudetest.FakeRegistry(t)
	got := reg.Add(t, "chief", "")
	pns(t, "send", "--as", "peons-1-2", "chief", "second")
	if _, stderr, code := pns(t, "send", "--as", "peons-1-2", "chief", "third"); code != 0 {
		t.Fatalf("send: exit %d: %s", code, stderr)
	}
	lines := got() // the fake listener takes only the first ring, sent after "second"
	msg, _ := lines[0]["message"].(map[string]any)
	want := `[pns] 2 unread (peons-1-3, peons-1-2) — run: pns inbox   (reply with: pns send peons-1-2 "...")`
	if msg["content"] != want || lines[0]["priority"] != "next" {
		t.Fatalf("doorbell = %v %q\nwant %q", lines[0]["priority"], msg["content"], want)
	}
}

func TestSendCountsRepeatSenders(t *testing.T) {
	useTempLogbook(t)
	pns(t, "send", "--as", "chief", "peon-a", "one")
	got := claudetest.FakeRegistry(t).Add(t, "peon-a", "")
	pns(t, "send", "--as", "chief", "--now", "peon-a", "two")
	lines := got()
	msg, _ := lines[0]["message"].(map[string]any)
	want := `[pns] 2 unread (chief ×2) — run: pns inbox   (reply with: pns send chief "...")`
	if msg["content"] != want || lines[0]["priority"] != "now" {
		t.Fatalf("doorbell = %v %q\nwant now %q", lines[0]["priority"], msg["content"], want)
	}
}

func TestSendToAStoppedSessionQueuesTheMessage(t *testing.T) {
	useTempLogbook(t)
	out, stderr, code := pns(t, "send", "--as", "chief", "peon-a", "hello")
	if code != 0 || !strings.Contains(out, "queued; peon-a is not running") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if inbox, _, _ := pns(t, "inbox", "--as", "peon-a"); !strings.Contains(inbox, "hello") {
		t.Fatalf("inbox = %q", inbox)
	}
}

func TestSendWarnsWhenTheRingFailsButKeepsTheMessage(t *testing.T) {
	useTempLogbook(t)
	reg := claudetest.FakeRegistry(t)
	reg.Add(t, "peon-a", "")
	reg.Edit(t, func(e map[string]any) { e["messagingSocketPath"] = "/nonexistent/s.sock" })
	_, stderr, code := pns(t, "send", "--as", "chief", "peon-a", "hello")
	if code != 0 || !strings.Contains(stderr, "couldn't ring peon-a") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if inbox, _, _ := pns(t, "inbox", "--as", "peon-a"); !strings.Contains(inbox, "hello") {
		t.Fatalf("inbox = %q", inbox)
	}
}
