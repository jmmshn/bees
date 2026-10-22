package worker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"syscall"
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
	Argv []string
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
	Parent(pid int) (int, error)
	Command(pid int) (string, error)
	Listening(ctx context.Context, pids []int) (map[int][]int, error)
	Signal(p proc.Process, sig syscall.Signal) error // only while p is the recorded process
}

// SystemProcs is internal/proc.
type SystemProcs struct{}

func (SystemProcs) Alive(p proc.Process) (bool, error)          { return proc.Alive(p.PID, p.Start) }
func (SystemProcs) Start(pid int) (string, error)               { return proc.Start(pid) }
func (SystemProcs) Name(pid int) (string, error)                { return proc.Name(pid) }
func (SystemProcs) Exe(pid int) (string, error)                 { return proc.Exe(pid) }
func (SystemProcs) Descendants(pid int) ([]proc.Process, error) { return proc.Descendants(pid) }
func (SystemProcs) Parent(pid int) (int, error)                 { return proc.Parent(pid) }
func (SystemProcs) Command(pid int) (string, error)             { return proc.Command(pid) }
func (SystemProcs) Signal(p proc.Process, sig syscall.Signal) error {
	return proc.Signal(p, sig)
}
func (SystemProcs) Listening(ctx context.Context, pids []int) (map[int][]int, error) {
	return proc.Listening(ctx, pids)
}

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
		var lastErr error // a lookup can fail for a moment (a process exiting mid-read): look again
		for {
			p, ok, err := harnessIn(ctx, h, ps, pane, kind)
			if err != nil && !errors.Is(err, ErrPaneGone) {
				lastErr, ok = err, false
			} else if err == nil {
				lastErr = nil
			}
			if ok {
				f := found{p: p}
				f.exe, _ = ps.Exe(p.PID) // informational
				f.baseline, f.baselineErr = ps.Descendants(p.PID)
				out <- f
				return
			}
			if time.Now().After(deadline) {
				out <- found{err: lastErr} // none: the start is unconfirmed, or the lookup kept failing
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

// ErrGuest: the recorded workspace holds a pane that runs another session's recorded
// process, moved in from elsewhere (herdr pane move): closing it would end that session.
var ErrGuest = errors.New("the workspace holds another session's pane")

// guests names the harness processes in sp's workspace outside sp's own recorded pane:
// another session's pane moved in with herdr pane move (it gets a new id, so it is never
// sp.Pane), of this project or any other, recorded or not. Closing the workspace would
// kill them (motif 12). A process this logbook recorded is named by its session. Any
// lookup that fails is an error, never "none" (motif 7).
func (f Fleet) guests(ctx context.Context, sp logbook.Spawn) ([]string, error) {
	panes, err := f.H.Panes(ctx, sp.Workspace)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, pane := range panes {
		if pane == sp.Pane {
			continue
		}
		ps, err := f.inPane(ctx, pane)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			name, err := f.Procs.Name(p.PID)
			if errors.Is(err, proc.ErrGone) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if name != f.Harness.Kind() {
				continue
			}
			g := fmt.Sprintf("a %s process (pid %d, pane %s)", name, p.PID, pane)
			if who, err := f.recordedAs(ctx, p); err != nil {
				return nil, err
			} else if who != "" {
				g = fmt.Sprintf("%s (pid %d, pane %s)", who, p.PID, pane)
			}
			if !slices.Contains(out, g) { // a foreground process is also its shell's descendant
				out = append(out, g)
			}
		}
	}
	return out, nil
}

// recordedAs is the session whose newest start or queen entry recorded p, if any.
func (f Fleet) recordedAs(ctx context.Context, p proc.Process) (string, error) {
	q, ok, err := f.LB.LatestQueen(ctx, f.P.Queen())
	if err != nil {
		return "", err
	}
	if ok && q.PID == p.PID && q.Start == p.Start {
		return q.Name, nil
	}
	for _, ended := range []bool{false, true} {
		var sps []logbook.Spawn
		var err error
		if ended {
			sps, err = f.LB.EndedSpawns(ctx)
		} else {
			sps, err = f.LB.LiveSpawns(ctx)
		}
		if err != nil {
			return "", err
		}
		for _, sp := range sps {
			st, ok, err := f.LB.LatestStart(ctx, sp.ID)
			if err != nil {
				return "", err
			}
			if ok && st.Process == p {
				return sp.Name, nil
			}
		}
	}
	return "", nil
}

// inPane is every process in pane: its foreground processes and its shell's descendants.
func (f Fleet) inPane(ctx context.Context, pane string) ([]proc.Process, error) {
	pi, err := f.H.ProcessInfo(ctx, pane)
	if errors.Is(err, ErrPaneGone) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []proc.Process
	for _, fg := range pi.Foreground {
		start, err := f.Procs.Start(fg.PID)
		if errors.Is(err, proc.ErrGone) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, proc.Process{PID: fg.PID, Start: start})
	}
	if pi.Shell > 0 {
		ds, err := f.Procs.Descendants(pi.Shell)
		if err != nil {
			return nil, err
		}
		out = append(out, ds...)
	}
	return out, nil
}

// stopWait bounds each wait for a stopped process to exit.
var stopWait = 5 * time.Second

// stop ends the recorded process p wherever it runs now (a moved pane), for bs end
// --force (bees-12.46): it closes the herdr pane that holds it, else sends it SIGTERM,
// then SIGKILL, each only while p is still the recorded process. It says how, or errs
// when p still runs: then the end must not be recorded (motif 7).
func (f Fleet) stop(ctx context.Context, p proc.Process) (string, error) {
	gone := func() bool {
		deadline := time.Now().Add(stopWait)
		for {
			alive, err := f.Procs.Alive(p)
			if err == nil && !alive {
				return true
			}
			if time.Now().After(deadline) {
				return false
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(watchEvery):
			}
		}
	}
	panes, err := f.H.Panes(ctx, "")
	if err != nil {
		return "", fmt.Errorf("can't find the pane it runs in: %w", err)
	}
	for _, pane := range panes {
		ps, err := f.inPane(ctx, pane)
		if err != nil {
			return "", err
		}
		if slices.Contains(ps, p) {
			if err := f.H.ClosePane(ctx, pane); err != nil {
				return "", fmt.Errorf("closing pane %s: %w", pane, err)
			}
			if gone() {
				return "closed pane " + pane, nil
			}
			break
		}
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		if err := f.Procs.Signal(p, sig); errors.Is(err, proc.ErrGone) {
			return "it exited", nil
		} else if err != nil {
			return "", err
		}
		if gone() {
			return fmt.Sprintf("sent it %s (pid %d)", sig, p.PID), nil
		}
	}
	return "", fmt.Errorf("pid %d still runs after SIGKILL", p.PID)
}
