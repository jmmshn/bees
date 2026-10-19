package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestMain runs the tests outside Claude even when a Claude session runs them: its
// session env would name a session the tests' fake HOME has never heard of (bees-8.8).
func TestMain(m *testing.M) {
	os.Unsetenv("CLAUDE_PID")
	os.Unsetenv("CLAUDE_CODE_SESSION_ID")
	os.Exit(m.Run())
}

func TestNoArgsPrintsUsageAndFails(t *testing.T) {
	var out, stderr bytes.Buffer
	code := run(nil, strings.NewReader(""), &out, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	for _, cmd := range []string{"init", "send", "inbox", "status"} {
		if !strings.Contains(stderr.String(), cmd) {
			t.Errorf("usage missing %q:\n%s", cmd, stderr.String())
		}
	}
}

func TestHelpPrintsUsageAndSucceeds(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := run([]string{"--help"}, strings.NewReader(""), &out, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "usage: bs") {
		t.Errorf("stdout = %q, want usage", out.String())
	}
}

func TestUnknownCommandFails(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := run([]string{"frobnicate"}, strings.NewReader(""), &out, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unknown command "frobnicate"`) {
		t.Errorf("stderr = %q", stderr.String())
	}
}
