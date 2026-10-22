package proc_test

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
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

// Descendants finds a grandchild with its start time, and Name names it.
func TestDescendantsFindsAGrandchild(t *testing.T) {
	c := exec.Command("sh", "-c", "sleep 30 & wait")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	var found proc.Process
	for deadline := time.Now().Add(5 * time.Second); found.PID == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		ds, err := proc.Descendants(c.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range ds {
			if n, _ := proc.Name(d.PID); n == "sleep" {
				found = d
			}
		}
	}
	if found.PID == 0 {
		t.Fatal("no sleep below sh")
	}
	if ok, err := proc.Alive(found.PID, found.Start); !ok || err != nil {
		t.Fatalf("Alive(%+v) = %v, %v: Descendants' start time must be Start's", found, ok, err)
	}
	if ds, err := proc.Descendants(found.PID); err != nil || len(ds) != 0 {
		t.Fatalf("Descendants(sleep) = %v, %v; want none", ds, err)
	}
}

// Listening finds a port this process listens on, and only for this process.
func TestListeningFindsOwnPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	got, err := proc.Listening(context.Background(), []int{os.Getpid(), os.Getppid()})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got[os.Getpid()], port) || slices.Contains(got[os.Getppid()], port) {
		t.Fatalf("Listening = %v, want port %d under pid %d only", got, port, os.Getpid())
	}
}

func TestCommandIsTheCommandLine(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	got, err := proc.Command(cmd.Process.Pid)
	for deadline := time.Now().Add(time.Second); err == nil && got != "sleep 30" && time.Now().Before(deadline); { // until the exec lands
		time.Sleep(10 * time.Millisecond)
		got, err = proc.Command(cmd.Process.Pid)
	}
	if err != nil || !strings.HasSuffix(got, "sleep 30") {
		t.Fatalf("Command = %q, %v; want sleep 30", got, err)
	}
	if _, err := proc.Command(1 << 22); !errors.Is(err, proc.ErrGone) {
		t.Fatalf("Command(dead) = %v, want ErrGone", err)
	}
}

// Age of a process just started is small, and of this test about its run time.
func TestAgeOfAFreshProcess(t *testing.T) {
	c := exec.Command("sleep", "5")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	if a, err := proc.Age(c.Process.Pid); err != nil || a < -time.Second || a > 3*time.Second {
		t.Fatalf("Age(fresh) = %s, %v", a, err)
	}
}

// A background process lists on one line, its command cut: Claude's shell wrapper runs to
// several lines and hundreds of characters (live test, bees-12.18).
func TestBackgroundListsOnOneLine(t *testing.T) {
	b := proc.Background{Process: proc.Process{PID: 42}, Command: "/usr/bin/zsh -c source snap.sh\n: && " + strings.Repeat("x", 200), Ports: []int{8000}}
	got := b.String()
	if strings.Contains(got, "\n") || !strings.HasPrefix(got, "pid 42     /usr/bin/zsh -c source snap.sh : && x") || !strings.HasSuffix(got, "x…   listening :8000") {
		t.Fatalf("String = %q", got)
	}
}
