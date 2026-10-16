package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// bs runs the CLI against a logbook in a temp dir and returns stdout, stderr and the exit code.
func bs(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return bsIn(t, "", args...)
}

// bsIn is bs with stdin.
func bsIn(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(stdin), &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func useTempLogbook(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // no real Claude registry: the session walk finds nothing
	t.Setenv("BS_LOGBOOK", filepath.Join(t.TempDir(), "logbook.db"))
	t.Setenv("BS_SESSION", "")
	t.Setenv("BS_PROJECT", "test")
	if _, stderr, code := bs(t, "init"); code != 0 {
		t.Fatalf("init: exit %d: %s", code, stderr)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	useTempLogbook(t)
	if _, stderr, code := bs(t, "init"); code != 0 {
		t.Fatalf("second init: exit %d: %s", code, stderr)
	}
}

func TestSendThenInboxShowsMessageOnce(t *testing.T) {
	useTempLogbook(t)
	if _, stderr, code := bs(t, "send", "--as", "queen", "worker-a", "add tests for parser"); code != 0 {
		t.Fatalf("send: exit %d: %s", code, stderr)
	}
	out, stderr, code := bs(t, "inbox", "--as", "worker-a")
	if code != 0 {
		t.Fatalf("inbox: exit %d: %s", code, stderr)
	}
	if !strings.Contains(out, "from queen") || !strings.Contains(out, "add tests for parser") {
		t.Fatalf("inbox output = %q", out)
	}
	again, _, _ := bs(t, "inbox", "--as", "worker-a")
	if again != "no unread messages\n" {
		t.Fatalf("second inbox = %q, want no unread", again)
	}
}

func TestInboxPeekLeavesMessagesUnread(t *testing.T) {
	useTempLogbook(t)
	bs(t, "send", "--as", "queen", "worker-a", "hello")
	for range 2 {
		out, _, code := bs(t, "inbox", "--as", "worker-a", "--peek")
		if code != 0 || !strings.Contains(out, "hello") {
			t.Fatalf("peek: exit %d, output %q", code, out)
		}
	}
}

func TestSendJoinsBodyWords(t *testing.T) {
	useTempLogbook(t)
	bs(t, "send", "--as", "queen", "worker-a", "rebase", "on", "main")
	out, _, _ := bs(t, "inbox", "--as", "worker-a")
	if !strings.Contains(out, "rebase on main") {
		t.Fatalf("inbox output = %q", out)
	}
}

func TestSendWithoutIdentityFails(t *testing.T) {
	useTempLogbook(t)
	_, stderr, code := bs(t, "send", "worker-a", "hi")
	if code != 2 || !strings.Contains(stderr, "--as") {
		t.Fatalf("exit %d, stderr %q; want exit 2 mentioning --as", code, stderr)
	}
}

func TestSendNeedsRecipientAndBody(t *testing.T) {
	useTempLogbook(t)
	_, stderr, code := bs(t, "send", "--as", "queen", "worker-a")
	if code != 2 || !strings.Contains(stderr, "usage: bs send") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

func TestSendWorksWithoutInit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BS_PROJECT", "test")
	t.Setenv("BS_LOGBOOK", filepath.Join(t.TempDir(), "logbook.db"))
	if _, stderr, code := bs(t, "send", "--as", "queen", "worker-a", "hi"); code != 0 {
		t.Fatalf("send: exit %d: %s", code, stderr)
	}
}

func TestFlagsMayFollowRecipient(t *testing.T) {
	useTempLogbook(t)
	if _, stderr, code := bs(t, "send", "worker-a", "--as", "queen", "hi"); code != 0 {
		t.Fatalf("send: exit %d: %s", code, stderr)
	}
	out, _, _ := bs(t, "inbox", "--as", "worker-a")
	if !strings.Contains(out, "from queen") {
		t.Fatalf("inbox output = %q", out)
	}
}

func TestSendDashReadsBodyFromStdin(t *testing.T) {
	useTempLogbook(t)
	if _, stderr, code := bsIn(t, "line one\nline two\n", "send", "--as", "queen", "worker-a", "-"); code != 0 {
		t.Fatalf("send: exit %d: %s", code, stderr)
	}
	out, _, _ := bs(t, "inbox", "--as", "worker-a")
	if !strings.Contains(out, "line one\nline two") {
		t.Fatalf("inbox output = %q", out)
	}
}

func TestSubcommandHelpExitsZero(t *testing.T) {
	useTempLogbook(t)
	for _, cmd := range []string{"send", "inbox"} {
		if _, stderr, code := bs(t, cmd, "-h"); code != 0 {
			t.Errorf("%s -h: exit %d: %s", cmd, code, stderr)
		}
	}
}

func TestInitRejectsArguments(t *testing.T) {
	useTempLogbook(t)
	if _, _, code := bs(t, "init", "junk"); code != 2 {
		t.Fatalf("init junk: exit %d, want 2", code)
	}
}

func TestEachProjectHasItsOwnLogbook(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BS_LOGBOOK", "")
	bs(t, "send", "--project", "alpha", "--as", "queen-alpha", "worker-1", "for alpha")
	out, _, _ := bs(t, "inbox", "--project", "beta", "--as", "worker-1")
	if out != "no unread messages\n" {
		t.Fatalf("beta inbox = %q, want empty", out)
	}
	out, _, _ = bs(t, "inbox", "--project", "alpha", "--as", "worker-1")
	if !strings.Contains(out, "for alpha") {
		t.Fatalf("alpha inbox = %q", out)
	}
}

func TestSpawnAndEndUsage(t *testing.T) {
	useTempLogbook(t)
	if _, _, code := bs(t, "spawn", "--as", "queen-test"); code != 2 {
		t.Errorf("spawn without bead: exit %d, want 2", code)
	}
	// No herdr, bd or Claude is reached: both are refused before anything is touched.
	for _, args := range [][]string{{"test-3"}, {"--label", "Fix Bug!", "test-3"}} {
		_, stderr, code := bs(t, append([]string{"spawn", "--as", "queen-test"}, args...)...)
		if code != 2 || !strings.Contains(stderr, "usage: bs spawn --label") || !strings.Contains(stderr, "squash merge end") {
			t.Errorf("spawn %q: exit %d, stderr %q; want usage with the label rules", args, code, stderr)
		}
	}
	_, stderr, code := bs(t, "end", "--as", "queen-test", "test-995-8")
	if code != 1 || !strings.Contains(stderr, "no record of spawning") {
		t.Errorf("end of never-spawned worker: exit %d, stderr %q", code, stderr)
	}
}
