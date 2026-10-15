package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// pns runs the CLI against a logbook in a temp dir and returns stdout, stderr and the exit code.
func pns(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func useTempLogbook(t *testing.T) {
	t.Helper()
	t.Setenv("PNS_LOGBOOK", filepath.Join(t.TempDir(), "logbook.db"))
	t.Setenv("PNS_SESSION", "")
	if _, stderr, code := pns(t, "init"); code != 0 {
		t.Fatalf("init: exit %d: %s", code, stderr)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	useTempLogbook(t)
	if _, stderr, code := pns(t, "init"); code != 0 {
		t.Fatalf("second init: exit %d: %s", code, stderr)
	}
}

func TestSendThenInboxShowsMessageOnce(t *testing.T) {
	useTempLogbook(t)
	if _, stderr, code := pns(t, "send", "--as", "chief", "peon-a", "add tests for parser"); code != 0 {
		t.Fatalf("send: exit %d: %s", code, stderr)
	}
	out, stderr, code := pns(t, "inbox", "--as", "peon-a")
	if code != 0 {
		t.Fatalf("inbox: exit %d: %s", code, stderr)
	}
	if !strings.Contains(out, "from chief") || !strings.Contains(out, "add tests for parser") {
		t.Fatalf("inbox output = %q", out)
	}
	again, _, _ := pns(t, "inbox", "--as", "peon-a")
	if again != "no unread messages\n" {
		t.Fatalf("second inbox = %q, want no unread", again)
	}
}

func TestInboxPeekLeavesMessagesUnread(t *testing.T) {
	useTempLogbook(t)
	pns(t, "send", "--as", "chief", "peon-a", "hello")
	for range 2 {
		out, _, code := pns(t, "inbox", "--as", "peon-a", "--peek")
		if code != 0 || !strings.Contains(out, "hello") {
			t.Fatalf("peek: exit %d, output %q", code, out)
		}
	}
}

func TestSendJoinsBodyWords(t *testing.T) {
	useTempLogbook(t)
	pns(t, "send", "--as", "chief", "peon-a", "rebase", "on", "main")
	out, _, _ := pns(t, "inbox", "--as", "peon-a")
	if !strings.Contains(out, "rebase on main") {
		t.Fatalf("inbox output = %q", out)
	}
}

func TestSendWithoutIdentityFails(t *testing.T) {
	useTempLogbook(t)
	_, stderr, code := pns(t, "send", "peon-a", "hi")
	if code != 2 || !strings.Contains(stderr, "--as") {
		t.Fatalf("exit %d, stderr %q; want exit 2 mentioning --as", code, stderr)
	}
}

func TestSendNeedsRecipientAndBody(t *testing.T) {
	useTempLogbook(t)
	_, stderr, code := pns(t, "send", "--as", "chief", "peon-a")
	if code != 2 || !strings.Contains(stderr, "usage: pns send") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}
