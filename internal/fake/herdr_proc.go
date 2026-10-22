package fake

import (
	"context"
	"syscall"

	"github.com/jmmshn/bees/internal/proc"
)

// procFaults are the process-table failures a test can turn on: each makes that lookup
// fail as a broken /proc or ps would, so the core must fail closed (motif 7).
type procFaults struct {
	AliveErr error // Alive and Start can't tell
	WalkErr  error // Descendants can't walk
}

// Alive is the real table's, unless AliveErr.
func (h *Herdr) Alive(p proc.Process) (bool, error) {
	if err := h.fault(func(f *procFaults) error { return f.AliveErr }); err != nil {
		return false, err
	}
	return proc.System{}.Alive(p)
}

func (h *Herdr) Start(pid int) (string, error) {
	if err := h.fault(func(f *procFaults) error { return f.AliveErr }); err != nil {
		return "", err
	}
	return proc.Start(pid)
}

func (h *Herdr) Parent(pid int) (int, error)     { return proc.Parent(pid) }
func (h *Herdr) Group(pid int) (int, error)      { return proc.Group(pid) }
func (h *Herdr) Command(pid int) (string, error) { return proc.Command(pid) }
func (h *Herdr) Exe(pid int) (string, error)     { return proc.Exe(pid) }

// Descendants is the real walk, unless WalkErr.
func (h *Herdr) Descendants(pid int) ([]proc.Process, error) {
	if err := h.fault(func(f *procFaults) error { return f.WalkErr }); err != nil {
		return nil, err
	}
	return proc.Descendants(pid)
}

// Signal is the real table's: only while p is still the process recorded.
func (h *Herdr) Signal(p proc.Process, sig syscall.Signal) error { return proc.Signal(p, sig) }

// Listening is the real table's.
func (h *Herdr) Listening(ctx context.Context, pids []int) (map[int][]int, error) {
	return proc.Listening(ctx, pids)
}

func (h *Herdr) fault(of func(*procFaults) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return of(&h.procFaults)
}

// FailStarts makes every Alive and Start lookup fail with err until it is called with
// nil: a process table bs can't read.
func (h *Herdr) FailStarts(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.AliveErr = err
}

// FailInfo makes the next n pane process-info calls fail as an unreachable server would.
func (h *Herdr) FailInfo(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.infoFails = n
}
