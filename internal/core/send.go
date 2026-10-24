package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
)

// SendIn is a bs send into this core's project P, as Resolve and May let it through.
type SendIn struct {
	From Caller
	// FromProject is the sender's project when the send crosses into P (T11): the reply
	// goes to its logbook. "" for a send within P.
	FromProject string
	To          string // a session name; a bead id names its worker
	Body        string
	Priority    port.Priority
	LogbookDir  string // every project's logbook dir: Addressed's, and the twin lock's
	// Bs is the shared bs on the sender's PATH, never a worker's build ("" when none),
	// and Session the herdr session of the config in effect: P's queen, started by a
	// crossing send, runs `<Bs> queen` there (OQ-K7, OQ-K8).
	Bs, Session string
}

// Sent is what a send did: the message is stored first (motif 10), then a crossing send
// to a queen that isn't running starts her, then she is rung.
type Sent struct {
	ID       logbook.ID
	Stranger bool          // neither P's queen nor one of its spawns: stored, never rung
	Started  logbook.Queen // the queen this send started; Pane "" when it started none
	Bs       string        // the bs that started her
	// NotStarted is why a crossing send found P's queen down and didn't start her;
	// ErrStarting when another send's start of her is still in progress.
	NotStarted error
	Rung       error // Ring's outcome
}

// ErrStarting is a queen start another send holds the twin lock for, past the wait.
var ErrStarting = errors.New("a queen start is in progress")

// Send stores in.Body in P's logbook, then rings. A send crossing projects goes queen to
// queen: a recorded session of another project may only message queen-P (exit 2, nothing
// stored), and when she isn't running the send starts her before it rings (ensureQueen).
// Send never makes a logbook: P has one (R48).
func (c *Core) Send(ctx context.Context, in SendIn) (Sent, error) {
	if strings.TrimSpace(in.Body) == "" {
		return Sent{}, exit(ExitRefused, bsname.Cmd+" send "+in.To+` "<message>"`, nil, "empty message; nothing stored")
	}
	queen := Queen(c.Project)
	if in.FromProject != "" && in.From.Recorded() && in.To != queen {
		return Sent{}, exit(ExitRefused, bsname.Cmd+" send --project "+c.Project+" "+queen+` "..."`, nil,
			"%s is of project %s: across projects, message %s, the queen of %s, rather than %s; nothing stored", in.From.Name, in.FromProject, queen, c.Project, in.To)
	}
	stranger, err := c.Addressed(ctx, in.To, in.LogbookDir)
	if err != nil {
		return Sent{}, err
	}
	out := Sent{Stranger: stranger}
	if out.ID, err = c.Logbook.SendFrom(ctx, in.From.Name, in.FromProject, in.To, in.Body); err != nil || stranger {
		return out, err
	}
	if in.FromProject != "" && in.To == queen {
		q, started, err := c.ensureQueen(ctx, in)
		switch {
		case err != nil:
			out.NotStarted = err
		case started:
			out.Started, out.Bs = q, in.Bs
		}
	}
	out.Rung = c.Ring(ctx, in.From.Name, in.To, in.Priority)
	// A queen just started may not take a ring yet (her harness still booting, or at a
	// prompt such as folder trust): try again until Timing.Confirm runs out.
	for deadline := time.Now().Add(c.timing().Confirm); out.Started.Pane != "" && out.Rung != nil && time.Now().Before(deadline); {
		if err := sleep(ctx, c.timing().Poll); err != nil {
			return out, nil
		}
		out.Rung = c.Ring(ctx, in.From.Name, in.To, in.Priority)
	}
	return out, nil
}

// ensureQueen returns P's queen, running: her latest entry when it is alive, else one
// this call started, in a new herdr workspace on P's repo whose shell runs `<in.Bs>
// queen` on in.Session, confirmed by her entry appearing alive within Timing.Confirm. The
// twin lock, flock(2) on <logbook dir>/<P>.queen.lock, is held across the check, the
// start and the confirmation, so a second send waits, then finds her running (motif 7);
// the kernel drops it when its holder dies, so a crashed send never wedges P (motif 5).
// The wait for it is bounded (motif 15): past it, ErrStarting. bs queen's own twin
// refusal stays the backstop against a queen started by hand meanwhile.
func (c *Core) ensureQueen(ctx context.Context, in SendIn) (q logbook.Queen, started bool, err error) {
	t := c.timing()
	name := Queen(c.Project)
	unlock, err := lock(ctx, filepath.Join(in.LogbookDir, c.Project+".queen.lock"), t.Confirm+t.AfterErr, t.Poll)
	if err != nil {
		return q, false, err
	}
	defer unlock()
	alive := func() (logbook.Queen, bool, error) {
		q, ok, err := c.Logbook.LatestQueen(ctx, name)
		if err != nil || !ok {
			return q, false, err
		}
		up, err := c.Proc.Alive(q.Process)
		if err != nil {
			return q, false, fmt.Errorf("can't tell whether %s runs (pid %d): %w", name, q.PID, err)
		}
		return q, up, nil
	}
	last, up, err := alive()
	switch {
	case err != nil:
		return q, false, err
	case up:
		return last, false, nil
	case in.Bs == "":
		return q, false, fmt.Errorf("no %s on PATH outside worker.path and the sender's workspace to start her with (a worker's build never starts another project's queen)", bsname.Cmd)
	}
	repo := cmp.Or(c.Repo, last.Repo)
	if repo == "" {
		return q, false, fmt.Errorf("project %s has no repo bs knows of to start her in", c.Project)
	}
	if fi, err := os.Stat(repo); err != nil || !fi.IsDir() { // herdr opens a missing dir as $HOME
		return q, false, fmt.Errorf("her repo %s isn't a directory: %v", repo, err)
	}
	h := c.herdr(in.Session)
	ws, err := h.CreateWorkspace(ctx, repo, "queen "+c.Project)
	if err != nil {
		return q, false, fmt.Errorf("herdr --session %s workspace create: %w", in.Session, err)
	}
	if err := h.RunInPane(ctx, ws.Pane, shellQuote(in.Bs)+" queen"); err != nil {
		return q, false, fmt.Errorf("herdr --session %s pane run %s: %w", in.Session, ws.Pane, err)
	}
	for deadline := time.Now().Add(t.Confirm); ; {
		if q, up, err := alive(); err != nil {
			return q, false, err
		} else if up {
			return q, true, nil
		}
		if time.Now().After(deadline) {
			return q, false, fmt.Errorf("%s queen ran in pane %s on herdr session %s, but no live %s entry within %s (herdr --session %s pane read %s --source recent)",
				in.Bs, ws.Pane, in.Session, name, t.Confirm, in.Session, ws.Pane)
		}
		if err := sleep(ctx, t.Poll); err != nil {
			return q, false, err
		}
	}
}

// lock takes an exclusive flock(2) on path, made if missing, polling every poll for up to
// wait: past it, ErrStarting (the queen's twin lock, a command workspace's spawn lock).
// unlock releases it.
func lock(ctx context.Context, path string, wait, poll time.Duration) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	for deadline := time.Now().Add(wait); ; {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		switch {
		case !errors.Is(err, syscall.EWOULDBLOCK):
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		case time.Now().After(deadline):
			f.Close()
			return nil, ErrStarting
		}
		if err := sleep(ctx, poll); err != nil {
			f.Close()
			return nil, err
		}
	}
}

// shellQuote is s as one word of a POSIX shell line.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
