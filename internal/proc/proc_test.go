package proc_test

import (
	"os"
	"testing"

	"github.com/jmmshn/bees/internal/proc"
)

func TestParentIsGetppid(t *testing.T) {
	if got := proc.Parent(os.Getpid()); got != os.Getppid() {
		t.Fatalf("Parent(self) = %d, want %d", got, os.Getppid())
	}
	// The walk bs does: a grandparent must be readable too, not only Getppid.
	if got := proc.Parent(os.Getppid()); got <= 0 {
		t.Fatalf("Parent(ppid) = %d, want a pid", got)
	}
}

func TestStartIsStableAndFailsForADeadPid(t *testing.T) {
	a, err := proc.Start(os.Getpid())
	if err != nil || a == "" {
		t.Fatalf("Start(self) = %q, %v", a, err)
	}
	if b, _ := proc.Start(os.Getpid()); b != a {
		t.Fatalf("Start changed: %q then %q", a, b)
	}
	if s, err := proc.Start(1 << 22); err == nil { // above any pid_max
		t.Fatalf("Start(dead) = %q, want an error", s)
	}
}
