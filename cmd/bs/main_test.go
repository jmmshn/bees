package main

import (
	"bytes"
	"strings"
	"testing"
)

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
