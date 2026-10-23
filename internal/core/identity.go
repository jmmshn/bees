package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/proc"
)

// Who is the session calling bs, as identity found it: a queen, a worker (an open spawn),
// or, with neither, the keeper.
type Who struct {
	Name    string
	Queen   bool
	Spawn   logbook.ID // a worker's open spawned entry
	Process proc.Process
}

// Keeper is the session bs takes a caller with no recorded ancestor for (ADR-0010).
const Keeper = "keeper"

// Role is what w may do (ADR-0010 Identity): "queen", "worker" or "keeper".
func (w Who) Role() string {
	switch {
	case w.Queen:
		return "queen"
	case w.Spawn != "":
		return "worker"
	}
	return Keeper
}

// Pane is the session the scan found: the open entry that recorded the caller, in its
// project's logbook.
type Pane struct {
	Session, Pane string // where the entry recorded it
	Project       string // the logbook's file name without .db
	Logbook       string
	Who           Who
	Repo          string // the recorded repo: a worker's workspace's, a queen's own
	Unconfirmed   bool   // a worker with no live start (none yet, or its process died), whose harness runs above bs: Identify heals it
	Tripwire      bool   // no recorded ancestor, but an unrecorded pane in a live worker's herdr workspace: taken for that worker
}

// ScanIn is what ScanPanes reads: the calling bs, its pane hint, and the ports to look with.
type ScanIn struct {
	Dir           string // paths.logbook_dir: every <project>.db in it is read, read-only
	Session, Pane string // HERDR_SESSION (or default) and HERDR_PANE_ID: hints, never identity
	Self          int
	Run           port.Runner
	Proc          port.Proc
	Program       func(harness string) string // a recorded harness's process name; nil skips the heal probe
}

// ScanPanes finds who calls bs (ADR-0010 identity): the nearest of self's ancestors that
// an open entry recorded (a queen's process, an open spawn's start), scanning every
// logbook in in.Dir read-only: <project>.db only (a .db.pre-N copy is skipped), an
// unreadable one an error naming it. HERDR_PANE_ID is only a hint, used where no ancestor
// is recorded: a worker entry of that pane with no live start counts when its harness, with
// --name <worker> in its argv, runs in the pane above in.Self (its start is healed when its
// logbook is opened); else a pane in a live worker's herdr workspace is taken for that
// worker (the tripwire). Two entries at the same distance is an error (motif 4); none is ok
// false, and the caller is the keeper. Any lookup that fails is an error, never "nobody"
// (motif 7).
func ScanPanes(ctx context.Context, in ScanIn) (Pane, bool, error) {
	es, err := os.ReadDir(in.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return Pane{}, false, nil
	} else if err != nil {
		return Pane{}, false, exit(ExitCantCheck, bsname.Cmd+" doctor", err, bsname.Cmd+" can't read the logbook dir %s to find who runs it", in.Dir)
	}
	up, err := ancestors(in.Proc, in.Self)
	if err != nil {
		return Pane{}, false, exit(ExitCantCheck, bsname.Cmd+" doctor", err, bsname.Cmd+" can't walk its own ancestors")
	}
	var found, healed, tripped []Pane
	near := len(up)                // the nearest recorded ancestor's index in up
	var probe *herdr.PaneProcesses // the hinted pane's processes, looked up once, for a heal
	var paneWS *string             // the hinted pane's workspace, looked up once, for the tripwire
	for _, e := range es {
		project, ok := strings.CutSuffix(e.Name(), ".db")
		if !ok || e.IsDir() {
			continue
		}
		path := filepath.Join(in.Dir, e.Name())
		recs, err := sessions(ctx, path)
		if err != nil {
			return Pane{}, false, exit(ExitCantCheck, "check "+path+" (a logbook dir holds only <project>.db logbooks)", err, bsname.Cmd+" can't read logbook %s to find who runs it", path)
		}
		for _, r := range recs {
			p := Pane{Session: r.Session, Pane: r.Pane, Project: project, Logbook: path,
				Who: Who{Name: r.Name, Queen: r.Queen, Spawn: r.Spawn, Process: r.Process}, Repo: r.Repo}
			if i := slices.Index(up, r.Process.PID); r.HasProcess && i >= 0 && i <= near {
				switch alive, err := in.Proc.Alive(r.Process); {
				case err != nil:
					return Pane{}, false, exit(ExitCantCheck, bsname.Cmd+" doctor", err, bsname.Cmd+" can't tell whether %s's process runs", r.Name)
				case alive && i < near:
					near, found = i, []Pane{p}
					continue
				case alive:
					found = append(found, p)
					continue
				}
			}
			if in.Pane == "" || r.Queen || r.Session != in.Session || len(found) > 0 {
				continue
			}
			if r.Pane == in.Pane && (!r.HasProcess || !alive(in.Proc, r.Process)) && in.Program != nil && !r.Legacy { // the old bs's worker: never healed (SPEC7)
				// A worker with no start, or whose recorded process died (restarted by hand in
				// its pane), counts when its harness runs here above in.Self: Identify heals it.
				if probe == nil {
					pi, err := (herdr.Client{Run: in.Run, Session: in.Session}).ProcessInfo(ctx, in.Pane)
					if err != nil && !errors.Is(err, herdr.ErrPaneGone) {
						return Pane{}, false, exit(ExitCantCheck, "herdr --session "+in.Session+" pane process-info --pane "+in.Pane, err, "pane %s holds %s, whose start was never confirmed, and "+bsname.Cmd+" can't look for its harness", in.Pane, r.Name)
					}
					probe = &pi
				}
				if slices.ContainsFunc(probe.Foreground, func(f herdr.PaneProcess) bool {
					return isOwn(f, in.Program(r.Harness), r.Name) && slices.Contains(up, f.PID)
				}) {
					p.Unconfirmed = true
					healed = append(healed, p)
					continue
				}
			}
			if r.Workspace == "" {
				continue
			}
			if paneWS == nil {
				ws, _, err := (herdr.Client{Run: in.Run, Session: in.Session}).PaneWorkspace(ctx, in.Pane)
				if err != nil {
					return Pane{}, false, exit(ExitCantCheck, "herdr --session "+in.Session+" pane list", err, "pane %s is recorded by nobody, and "+bsname.Cmd+" can't tell whether it is in a worker's workspace", in.Pane)
				}
				paneWS = &ws
			}
			if *paneWS == r.Workspace {
				p.Tripwire = true
				tripped = append(tripped, p)
			}
		}
	}
	for _, ps := range [][]Pane{found, healed, tripped} {
		switch len(ps) {
		case 0:
			continue
		case 1:
			return ps[0], true, nil
		}
		var names []string
		for _, p := range ps {
			names = append(names, p.Who.Name+" ("+p.Logbook+")")
		}
		return Pane{}, false, exit(ExitFail, bsname.Cmd+" status, then "+bsname.Cmd+" end the one that shouldn't be there", nil, "this bs is claimed by %d live sessions: %s", len(ps), strings.Join(names, ", "))
	}
	return Pane{}, false, nil
}

// alive is whether p runs; a process bs can't look at is taken for running, so the
// caller falls to the tripwire rather than a heal.
func alive(ps port.Proc, p proc.Process) bool {
	ok, err := ps.Alive(p)
	return ok || err != nil
}

// recorded is a logbook.Recorded with its spawn's workspace repo, and whether the spawn
// is the old bs's (legacy), which is never healed.
type recorded struct {
	logbook.Recorded
	Repo   string
	Legacy bool
}

// sessions reads every open entry of the logbook at path.
func sessions(ctx context.Context, path string) ([]recorded, error) {
	lb, err := logbook.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, err
	}
	defer lb.Close()
	recs, err := lb.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]recorded, len(recs))
	for i, r := range recs {
		out[i].Recorded = r
		if r.Queen {
			out[i].Repo = r.Repo
			continue
		}
		sp, err := lb.Spawn(ctx, r.Spawn)
		if err != nil {
			return nil, err
		}
		out[i].Repo, out[i].Legacy = sp.Workspace.Repo, sp.Legacy()
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
		return Who{}, exit(ExitRefused, bsname.Cmd+" status", nil, "%s's entry for pane %s closed while bs looked", p.Who.Name, p.Pane)
	}
	r := recs[i]
	if p.Tripwire { // no ancestor to confirm: the workspace was the evidence
		c.warn("no recorded session runs this bs, but its herdr pane is in %s's workspace %s: taken for %s, with a worker's permissions", r.Name, r.Workspace, r.Name)
		return Who{Name: r.Name, Spawn: r.Spawn, Process: r.Process}, nil
	}
	if !r.HasProcess || p.Unconfirmed {
		sp, err := c.Logbook.Spawn(ctx, r.Spawn)
		if err != nil {
			return Who{}, err
		}
		st, ok, err := c.StartOf(ctx, sp)
		switch {
		case err != nil:
			return Who{}, exit(ExitCantCheck, "herdr --session "+p.Session+" pane process-info --pane "+p.Pane, err, "pane %s holds %s, whose start was never confirmed", p.Pane, r.Name)
		case !ok:
			return Who{}, exit(ExitStart, bsname.Cmd+" end --incomplete "+r.Name, nil, "pane %s holds %s, whose start was never confirmed and whose harness "+bsname.Cmd+" can't find there", p.Pane, r.Name)
		}
		r.Process = st.Process
	}
	up, err := c.ancestors(self)
	if err != nil {
		return Who{}, exit(ExitCantCheck, bsname.Cmd+" doctor", err, bsname.Cmd+" can't walk its own ancestors")
	}
	switch alive, err := c.Proc.Alive(r.Process); {
	case err != nil:
		return Who{}, exit(ExitCantCheck, bsname.Cmd+" doctor", err, bsname.Cmd+" can't tell whether %s's process runs", r.Name)
	case !alive || !slices.Contains(up, r.Process.PID):
		return Who{}, exit(ExitRefused, bsname.Cmd+" status", nil, "%s's process (pid %d) is no longer this bs's ancestor", r.Name, r.Process.PID)
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
