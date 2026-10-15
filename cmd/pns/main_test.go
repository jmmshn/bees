package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestNoArgsPrintsUsageAndFails(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(nil, &out, &errb)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	for _, cmd := range []string{"init", "send", "inbox", "status"} {
		if !strings.Contains(errb.String(), cmd) {
			t.Errorf("usage missing %q:\n%s", cmd, errb.String())
		}
	}
}

func TestHelpPrintsUsageAndSucceeds(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--help"}, &out, &errb); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "usage: pns") {
		t.Errorf("stdout = %q, want usage", out.String())
	}
}

func TestUnknownCommandFails(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"frobnicate"}, &out, &errb); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), `unknown command "frobnicate"`) {
		t.Errorf("stderr = %q", errb.String())
	}
}
