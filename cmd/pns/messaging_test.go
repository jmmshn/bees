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
	return pnsIn(t, "", args...)
}

// pnsIn is pns with stdin.
func pnsIn(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(stdin), &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func useTempLogbook(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // no real Claude registry: the session walk finds nothing
	t.Setenv("PNS_LOGBOOK", filepath.Join(t.TempDir(), "logbook.db"))
	t.Setenv("PNS_SESSION", "")
	t.Setenv("PNS_PROJECT", "test")
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

func TestSendWorksWithoutInit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PNS_PROJECT", "test")
	t.Setenv("PNS_LOGBOOK", filepath.Join(t.TempDir(), "logbook.db"))
	if _, stderr, code := pns(t, "send", "--as", "chief", "peon-a", "hi"); code != 0 {
		t.Fatalf("send: exit %d: %s", code, stderr)
	}
}

func TestFlagsMayFollowRecipient(t *testing.T) {
	useTempLogbook(t)
	if _, stderr, code := pns(t, "send", "peon-a", "--as", "chief", "hi"); code != 0 {
		t.Fatalf("send: exit %d: %s", code, stderr)
	}
	out, _, _ := pns(t, "inbox", "--as", "peon-a")
	if !strings.Contains(out, "from chief") {
		t.Fatalf("inbox output = %q", out)
	}
}

func TestSendDashReadsBodyFromStdin(t *testing.T) {
	useTempLogbook(t)
	if _, stderr, code := pnsIn(t, "line one\nline two\n", "send", "--as", "chief", "peon-a", "-"); code != 0 {
		t.Fatalf("send: exit %d: %s", code, stderr)
	}
	out, _, _ := pns(t, "inbox", "--as", "peon-a")
	if !strings.Contains(out, "line one\nline two") {
		t.Fatalf("inbox output = %q", out)
	}
}

func TestSubcommandHelpExitsZero(t *testing.T) {
	useTempLogbook(t)
	for _, cmd := range []string{"send", "inbox"} {
		if _, stderr, code := pns(t, cmd, "-h"); code != 0 {
			t.Errorf("%s -h: exit %d: %s", cmd, code, stderr)
		}
	}
}

func TestInitRejectsArguments(t *testing.T) {
	useTempLogbook(t)
	if _, _, code := pns(t, "init", "junk"); code != 2 {
		t.Fatalf("init junk: exit %d, want 2", code)
	}
}

func TestEachProjectHasItsOwnLogbook(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PNS_LOGBOOK", "")
	pns(t, "send", "--project", "alpha", "--as", "chief-alpha", "peon-1", "for alpha")
	out, _, _ := pns(t, "inbox", "--project", "beta", "--as", "peon-1")
	if out != "no unread messages\n" {
		t.Fatalf("beta inbox = %q, want empty", out)
	}
	out, _, _ = pns(t, "inbox", "--project", "alpha", "--as", "peon-1")
	if !strings.Contains(out, "for alpha") {
		t.Fatalf("alpha inbox = %q", out)
	}
}

func TestSpawnAndEndUsage(t *testing.T) {
	useTempLogbook(t)
	if _, _, code := pns(t, "spawn", "--as", "chief-test"); code != 2 {
		t.Errorf("spawn without bead: exit %d, want 2", code)
	}
	_, stderr, code := pns(t, "end", "--as", "chief-test", "peon-995-8")
	if code != 1 || !strings.Contains(stderr, "no record of spawning") {
		t.Errorf("end of never-spawned peon: exit %d, stderr %q", code, stderr)
	}
}
