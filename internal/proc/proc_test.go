package proc_test

import (
	"errors"
	"os"
	"os/exec"
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

// Alive is this process with its own start time, not with another, and not an exited pid.
func TestAliveChecksTheStartTime(t *testing.T) {
	s, err := proc.Start(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := proc.Alive(os.Getpid(), s); !ok || err != nil {
		t.Fatalf("Alive(self) = %v, %v", ok, err)
	}
	if ok, err := proc.Alive(os.Getpid(), s+"0"); ok || err != nil {
		t.Fatalf("Alive(self, other start) = %v, %v; want a reused pid read as gone", ok, err)
	}
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if ok, err := proc.Alive(c.Process.Pid, s); ok || err != nil {
		t.Fatalf("Alive(exited) = %v, %v", ok, err)
	}
}
