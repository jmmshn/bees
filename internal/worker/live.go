package worker

import (
	"context"
	"errors"
	"time"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
)

// Liveness is the recorded harness process (ADR-0009 amendment 1): a session is Live while
// the process its start recorded is alive with its recorded start time, else Absent. herdr
// is asked only, at start, which process runs in the pane; its agent and agent_status are
// never read.

var (
	// ErrPaneGone: herdr has no pane by that id.
	ErrPaneGone = errors.New("pane not found")
	// ErrMoved: the recorded workspace is gone but the worker's process still runs, so
	// someone moved its pane (herdr pane move gives it a new id).
	ErrMoved = errors.New("the worker still runs outside its recorded workspace")
)

// PaneProcess is a process herdr reports in a pane.
type PaneProcess struct {
	PID  int
	Name string
}

// PaneProcesses is herdr's process-info for a pane.
type PaneProcesses struct {
	Shell      int // the pane's shell
	Foreground []PaneProcess
}

// Procs reads the process table. SystemProcs is the real one; tests fake it.
type Procs interface {
	Alive(p proc.Process) (bool, error)
	Start(pid int) (string, error)
	Name(pid int) (string, error)
	Exe(pid int) (string, error)
	Descendants(pid int) ([]proc.Process, error)
}

// SystemProcs is internal/proc.
type SystemProcs struct{}

func (SystemProcs) Alive(p proc.Process) (bool, error)          { return proc.Alive(p.PID, p.Start) }
func (SystemProcs) Start(pid int) (string, error)               { return proc.Start(pid) }
func (SystemProcs) Name(pid int) (string, error)                { return proc.Name(pid) }
func (SystemProcs) Exe(pid int) (string, error)                 { return proc.Exe(pid) }
func (SystemProcs) Descendants(pid int) ([]proc.Process, error) { return proc.Descendants(pid) }

// SpawnAlive reports whether open spawn sp's worker runs: its newest start's process is
// alive with its start time (SEC-2). known is false when sp has no start recorded: spawned
// before bs recorded starts, or never confirmed, so it reads as Absent; a start then looks
// in the pane before it launches (Fleet.launch), so no twin starts beside one still there.
// Any failed lookup is an error, never Absent (motif 7).
func SpawnAlive(ctx context.Context, lb *logbook.Logbook, ps Procs, sp logbook.Spawn) (alive, known bool, st logbook.Start, err error) {
	st, known, err = lb.LatestStart(ctx, sp.ID)
	if err != nil || !known {
		return false, known, st, err
	}
	alive, err = ps.Alive(st.Process)
	return alive, true, st, err
}

// QueenAlive reports whether queen name's newest queen entry names a live process. known
// is false when the logbook has no queen entry: a queen started before bs recorded queens.
func QueenAlive(ctx context.Context, lb *logbook.Logbook, ps Procs, name string) (alive, known bool, err error) {
	q, ok, err := lb.LatestQueen(ctx, name)
	if err != nil || !ok {
		return false, false, err
	}
	alive, err = ps.Alive(proc.Process{PID: q.PID, Start: q.Start})
	return alive, true, err
}

// harnessIn finds the harness process of kind in pane: a foreground process named kind,
// else the nearest descendant of the pane's shell so named.
func harnessIn(ctx context.Context, h Herdr, ps Procs, pane, kind string) (proc.Process, bool, error) {
	pi, err := h.ProcessInfo(ctx, pane)
	if err != nil {
		return proc.Process{}, false, err
	}
	pid := 0
	for _, p := range pi.Foreground {
		if p.Name == kind {
			pid = p.PID
			break
		}
	}
	if pid == 0 && pi.Shell > 0 {
		ds, err := ps.Descendants(pi.Shell)
		if err != nil {
			return proc.Process{}, false, err
		}
		for _, d := range ds {
			n, err := ps.Name(d.PID)
			if err != nil && !errors.Is(err, proc.ErrGone) {
				return proc.Process{}, false, err
			}
			if n == kind {
				pid = d.PID
				break
			}
		}
	}
	if pid == 0 {
		return proc.Process{}, false, nil
	}
	start, err := ps.Start(pid)
	if errors.Is(err, proc.ErrGone) {
		return proc.Process{}, false, nil
	}
	return proc.Process{PID: pid, Start: start}, err == nil, err
}

// watchEvery is how often a start looks for its harness process (about 2 ms per look).
var watchEvery = 250 * time.Millisecond

// found is a harness process a start saw in its pane, with its descendants at that moment.
type found struct {
	p           proc.Process
	exe         string
	baseline    []proc.Process
	baselineErr error // the descendants couldn't be read: the start is recorded all the same
	err         error
}

// watch polls pane until the harness process of kind shows, or until wait passes (a zero
// found) or ctx ends, and sends what it saw. It reads the baseline as soon as the process shows, so
// the doorbell's first tool calls are not yet running (ADR-0009 amendment 1, item 3).
// A receive on kick (or its close) makes it look again at once.
func watch(ctx context.Context, h Herdr, ps Procs, pane, kind string, wait time.Duration, kick <-chan struct{}) <-chan found {
	out := make(chan found, 1)
	go func() {
		deadline := time.Now().Add(wait)
		for {
			p, ok, err := harnessIn(ctx, h, ps, pane, kind)
			if err != nil && !errors.Is(err, ErrPaneGone) {
				out <- found{err: err}
				return
			}
			if ok {
				f := found{p: p}
				f.exe, _ = ps.Exe(p.PID) // informational
				f.baseline, f.baselineErr = ps.Descendants(p.PID)
				out <- f
				return
			}
			if time.Now().After(deadline) {
				out <- found{} // none: the start is unconfirmed
				return
			}
			select {
			case <-ctx.Done():
				out <- found{err: ctx.Err()}
				return
			case <-time.After(watchEvery):
			case <-kick:
				kick = nil // closed: look once at once, then poll again
			}
		}
	}()
	return out
}
