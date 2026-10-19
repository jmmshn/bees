package proc_test

import (
	"errors"
	"os"
	"testing"

	"github.com/jmmshn/bees/internal/proc"
)

func TestParentIsGetppid(t *testing.T) {
	if got, err := proc.Parent(os.Getpid()); err != nil || got != os.Getppid() {
		t.Fatalf("Parent(self) = %d, %v; want %d", got, err, os.Getppid())
	}
	// The walk bs does: a grandparent must be readable too, not only Getppid.
	if got, err := proc.Parent(os.Getppid()); err != nil || got <= 0 {
		t.Fatalf("Parent(ppid) = %d, %v; want a pid", got, err)
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
	if s, err := proc.Start(1 << 22); !errors.Is(err, proc.ErrGone) { // above any pid_max
		t.Fatalf("Start(dead) = %q, %v; want ErrGone", s, err)
	}
	if p, err := proc.Parent(1 << 22); !errors.Is(err, proc.ErrGone) {
		t.Fatalf("Parent(dead) = %d, %v; want ErrGone", p, err)
	}
}
