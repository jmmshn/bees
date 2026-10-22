package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmmshn/bees/cmd/bsx/internal/herdr"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

// Who is the session calling bs, as identity found it.
type Who struct {
	Name    string
	Queen   bool
	Spawn   logbook.ID // a worker's open spawned entry
	Process proc.Process
}

// ErrOutsidePane is a caller in no recorded herdr pane: tests, scripts, the keeper's
// shell (a herdr pane no entry recorded among them), where --as names the session
// (bees-13.14).
var ErrOutsidePane = errors.New("not in a recorded herdr pane")

// Pane is a herdr pane as the scan found it: the open entry that recorded it, in its
// project's logbook.
type Pane struct {
	Session, Pane string
	Project       string // the logbook's file name without .db
	Logbook       string
	Who           Who
	Repo          string // a worker's recorded workspace repo; "" for a queen
	Unconfirmed   bool   // a worker with no start yet, whose harness runs above bs: Identify heals it
}

// ScanIn is what ScanPanes reads: the pane, the calling bs, and the ports to look with.
type ScanIn struct {
	Dir           string // paths.logbook_dir: every <project>.db in it is read, read-only
	Session, Pane string // HERDR_SESSION (or default) and HERDR_PANE_ID
	Self          int
	Run           port.Runner
	Proc          port.Proc
	Kind          string // the harness's process name; "" skips the heal probe
}

// ScanPanes finds the open entry that recorded the calling pane (ADR-0010 identity),
// scanning every logbook in in.Dir read-only: <project>.db only (a .db.pre-N copy is
// skipped), an unreadable one an error naming it. An entry counts only while its
// recorded process is alive and an ancestor of in.Self. A worker entry with no start
// counts when its harness, with --name <worker> in its argv, runs in the pane above
// in.Self: its start is healed when its logbook is opened. Two entries counting is an
// error (motif 4); none is ok false, and the caller is outside a recorded pane. Any
// lookup that fails is an error, never "nobody" (motif 7).
func ScanPanes(ctx context.Context, in ScanIn) (Pane, bool, error) {
	es, err := os.ReadDir(in.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return Pane{}, false, nil
	} else if err != nil {
		return Pane{}, false, exit(ExitCantCheck, "bsx doctor", err, "bs can't read the logbook dir %s to find this pane's session", in.Dir)
	}
	up, err := ancestors(in.Proc, in.Self)
	if err != nil {
		return Pane{}, false, exit(ExitCantCheck, "bsx doctor", err, "bs can't walk its own ancestors")
	}
	var found []Pane
	var probe *herdr.PaneProcesses // the pane's processes, looked up once, for a heal
	for _, e := range es {
		project, ok := strings.CutSuffix(e.Name(), ".db")
		if !ok || e.IsDir() {
			continue
		}
		path := filepath.Join(in.Dir, e.Name())
		recs, err := inPane(ctx, path, in.Session, in.Pane)
		if err != nil {
			return Pane{}, false, exit(ExitCantCheck, "check "+path+" (a logbook dir holds only <project>.db logbooks)", err, "bs can't read logbook %s to find this pane's session", path)
		}
		for _, r := range recs {
			p := Pane{Session: in.Session, Pane: in.Pane, Project: project, Logbook: path,
				Who: Who{Name: r.Name, Queen: r.Queen, Spawn: r.Spawn, Process: r.Process}, Repo: r.Repo}
			if !r.HasProcess {
				if r.Queen || in.Kind == "" {
					continue
				}
				if probe == nil {
					pi, err := (herdr.Client{Run: in.Run, Session: in.Session}).ProcessInfo(ctx, in.Pane)
					if err != nil && !errors.Is(err, herdr.ErrPaneGone) {
						return Pane{}, false, exit(ExitCantCheck, "herdr --session "+in.Session+" pane process-info --pane "+in.Pane, err, "pane %s holds %s, whose start was never confirmed, and bs can't look for its harness", in.Pane, r.Name)
					}
					probe = &pi
				}
				i := slices.IndexFunc(probe.Foreground, func(f herdr.PaneProcess) bool {
					return f.Name == in.Kind && named(f.Argv, r.Name) && slices.Contains(up, f.PID)
				})
				if i < 0 {
					continue
				}
				p.Unconfirmed = true
				found = append(found, p)
				continue
			}
			alive, err := in.Proc.Alive(r.Process)
			if err != nil {
				return Pane{}, false, exit(ExitCantCheck, "bsx doctor", err, "bs can't tell whether %s's process runs", r.Name)
			}
			if alive && slices.Contains(up, r.Process.PID) {
				found = append(found, p)
			}
		}
	}
	switch len(found) {
	case 0:
		return Pane{}, false, nil
	case 1:
		return found[0], true, nil
	}
	var names []string
	for _, p := range found {
		names = append(names, p.Who.Name+" ("+p.Logbook+")")
	}
	return Pane{}, false, exit(ExitFail, "bsx status, then bsx end the one that shouldn't be there", nil, "pane %s on herdr session %s is recorded by %d live sessions: %s", in.Pane, in.Session, len(found), strings.Join(names, ", "))
}

// recorded is a logbook.Recorded with its spawn's workspace repo.
type recorded struct {
	logbook.Recorded
	Repo string
}

// inPane reads the entries of the logbook at path that recorded pane.
func inPane(ctx context.Context, path, session, pane string) ([]recorded, error) {
	lb, err := logbook.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, err
	}
	defer lb.Close()
	recs, err := lb.InPane(ctx, session, pane)
	if err != nil {
		return nil, err
	}
	out := make([]recorded, len(recs))
	for i, r := range recs {
		out[i].Recorded = r
		if r.Queen {
			continue
		}
		sp, err := lb.Spawn(ctx, r.Spawn)
		if err != nil {
			return nil, err
		}
		out[i].Repo = sp.Workspace.Repo
	}
	return out, nil
}

// Identify confirms p, the pane ScanPanes found, against this core's logbook, healing an
// unconfirmed worker's start (ADR-0010): the entry must still be open, and its process
// alive and an ancestor of self.
func (c *Core) Identify(ctx context.Context, p Pane, self int) (Who, error) {
	recs, err := c.Logbook.InPane(ctx, p.Session, p.Pane)
	if err != nil {
		return Who{}, err
	}
	i := slices.IndexFunc(recs, func(r logbook.Recorded) bool { return r.Name == p.Who.Name })
	if i < 0 {
		return Who{}, exit(ExitRefused, "bsx status", nil, "%s's entry for pane %s closed while bs looked", p.Who.Name, p.Pane)
	}
	r := recs[i]
	if !r.HasProcess {
		sp, err := c.Logbook.Spawn(ctx, r.Spawn)
		if err != nil {
			return Who{}, err
		}
		st, ok, err := c.StartOf(ctx, sp)
		switch {
		case err != nil:
			return Who{}, exit(ExitCantCheck, "herdr --session "+p.Session+" pane process-info --pane "+p.Pane, err, "pane %s holds %s, whose start was never confirmed", p.Pane, r.Name)
		case !ok:
			return Who{}, exit(ExitStart, "bs end "+r.Name+" --incomplete", nil, "pane %s holds %s, whose start was never confirmed and whose harness bs can't find there", p.Pane, r.Name)
		}
		r.Process = st.Process
	}
	up, err := c.ancestors(self)
	if err != nil {
		return Who{}, exit(ExitCantCheck, "bsx doctor", err, "bs can't walk its own ancestors")
	}
	switch alive, err := c.Proc.Alive(r.Process); {
	case err != nil:
		return Who{}, exit(ExitCantCheck, "bsx doctor", err, "bs can't tell whether %s's process runs", r.Name)
	case !alive || !slices.Contains(up, r.Process.PID):
		return Who{}, exit(ExitRefused, "bsx status", nil, "%s's process (pid %d) is no longer this bs's ancestor", r.Name, r.Process.PID)
	}
	return Who{Name: r.Name, Queen: r.Queen, Spawn: r.Spawn, Process: r.Process}, nil
}

// ancestors is self and every process above it.
func (c *Core) ancestors(self int) ([]int, error) { return ancestors(c.Proc, self) }

func ancestors(ps port.Proc, self int) ([]int, error) {
	out := []int{self}
	for pid := self; pid > 1; {
		pp, err := ps.Parent(pid)
		if errors.Is(err, proc.ErrGone) {
			break
		} else if err != nil {
			return nil, err
		}
		if pp <= 0 || slices.Contains(out, pp) {
			break
		}
		out, pid = append(out, pp), pp
	}
	return out, nil
}

// Getenv is key's value in env, the last one set winning, as the process would see it.
func Getenv(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}
