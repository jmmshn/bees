package proc_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

var started = time.Now()

// Start must match Claude's procStart byte for byte, independent of how bs gets it:
// the C locale's ctime layout, in UTC whatever the local zone (seen in the registry
// as "Sun Oct 18 03:33:19 2026" at 20:33 local time).
func TestStartIsClaudesUTCLstart(t *testing.T) {
	t.Setenv("TZ", "America/Los_Angeles")
	s, err := proc.Start(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", s, time.UTC)
	if err != nil {
		t.Fatalf("Start = %q: %v", s, err)
	}
	if d := started.Sub(at); d < -time.Second || d > 5*time.Second {
		t.Fatalf("Start = %q (%s), but the test started at %s", s, at, started.UTC())
	}
	if want := at.Format("Mon Jan _2 15:04:05 2006"); s != want {
		t.Fatalf("Start = %q, want %q", s, want)
	}
}

// A ps earlier on PATH must not answer: bs's sender check rests on it (bees-7.4).
func TestStartIgnoresAPsOnPath(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "ps")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'Z Mon Jan  1 00:00:00 2001'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, _ := exec.Command(fake).Output(); len(out) == 0 {
		t.Fatal("fake ps didn't run")
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if s, err := proc.Start(os.Getpid()); err != nil || s == "Mon Jan  1 00:00:00 2001" {
		t.Fatalf("Start = %q, %v: the fake ps answered", s, err)
	}
}
