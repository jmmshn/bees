package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/proc"
)

// Role is what a caller may do in the project a call acts in (ADR-0010 Amendment 4).
type Role string

const (
	KeeperR Role = "keeper"  // no recorded ancestor in any logbook bs can see
	QueenR  Role = "queen"   // P's queen entry recorded an ancestor
	WorkerR Role = "worker"  // an open spawn of P recorded an ancestor (or the tripwire)
	Foreign Role = "foreign" // a session another project recorded, acting in P
)

// Keeper is the name bs gives a caller with no recorded ancestor.
const Keeper = "keeper"

// How is how Resolve found a caller.
type How string

const (
	ByAncestor How = "ancestor"
	ByTripwire How = "tripwire"
	ByAs       How = "--as"
	ByNobody   How = "keeper"
)

// Caller is who bs takes the caller for, in project P (ADR-0010 Amendment 4 Identity).
type Caller struct {
	Name    string // queen-<p>, a worker's name, or "keeper"
	Role    Role
	Project string     // the project whose logbook recorded it; "" for the keeper
	Spawn   logbook.ID // a worker's open spawned entry
	Spawned time.Time  // when it was spawned
	Process proc.Process
	How     How
	Logbook string // the logbook that recorded it
	Repo    string // the repo it was recorded in: a worker's workspace's, a queen's own
	// Session and Pane are where its entry says herdr runs it.
	Session, Pane string
	Workspace     string // the herdr workspace the tripwire found it by
	Unconfirmed   bool   // a worker with no live start whose harness runs above bs: Confirm heals it
	Peek          bool   // --as from the keeper's shell: bs inbox marks nothing read

	harness, herdrWS, dir string // a worker's recorded harness, herdr workspace and workspace dir
	program               string // the program its spawn recorded its launches run
	legacy                bool   // the old bs's spawn, never healed
}

// Dir is a worker's workspace dir as its spawn recorded it; "" for any other caller.
func (c Caller) Dir() string { return c.dir }

// Recorded reports whether c is a session some logbook recorded, not the keeper.
func (c Caller) Recorded() bool { return c.Role != KeeperR && c.Role != "" }

// ResolveIn is everything Resolve looks at; nothing in it is a name the caller typed.
type ResolveIn struct {
	Project string   // P: the project of the repo bs runs in; "" outside any
	Logbook string   // P's logbook; "" when P is
	Cwd     string   // where bs runs: in another project's worker's workspace, that project is P
	Dirs    []string // every logbook dir bs can see: the config in effect's, then the default config's
	Self    int
	// Session and Pane are HERDR_SESSION (or default) and HERDR_PANE_ID: hints for the
	// heal and the tripwire, never identity.
	Session, Pane string
	Run           port.Runner
	Proc          port.Proc
	Harness       func(name string) port.Harness // a recorded harness's adapter (nil: none here); nil skips the heal probe
}

// Resolve finds who calls bs (ADR-0010 Amendment 4 Identity), reading every logbook
// read-only: the nearest of self's ancestors that P's logbook recorded (a queen's
// process, an open spawn's start, alive), else a worker of P with no live start whose
// harness runs in the hinted pane above self (Confirm heals it), else the tripwire (the
// hinted herdr pane, recorded by nobody, is in a live spawn's herdr workspace or
// directory: that worker), else the nearest ancestor another project recorded (Foreign;
// outside any repo, that session acting in its own project), else the keeper. A cwd in
// another project's open worker's workspace makes that project P first: a command
// workspace that is a checkout of its own (a clone, a client) names no project of its own
// (bees-14.6). Two
// sessions at the same distance is exit 1 naming both (motif 4); any lookup that fails is
// exit 6, never the keeper (motif 7).
func Resolve(ctx context.Context, in ResolveIn) (Caller, error) {
	up, err := ancestors(in.Proc, in.Self)
	if err != nil {
		return Caller{}, exit(ExitCantCheck, bsname.Cmd+" doctor", err, bsname.Cmd+" can't walk its own ancestors")
	}
	var mine []Caller // P's open sessions
	if in.Logbook != "" {
		if mine, err = sessions(ctx, in.Logbook, in.Project); err != nil {
			return Caller{}, err
		}
		if c, ok, err := nearest(in.Proc, up, mine); err != nil || ok {
			return c, err
		}
		if c, ok, err := heal(ctx, in, up, mine); err != nil || ok {
			return c, err
		}
		if c, ok, err := tripwire(ctx, in, mine); err != nil || ok {
			return c, err
		}
	}
	var others []Caller
	for _, dir := range in.Dirs {
		es, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return Caller{}, exit(ExitCantCheck, bsname.Cmd+" doctor", err, bsname.Cmd+" can't read the logbook dir %s to find who runs it", dir)
		}
		for _, e := range es {
			project, ok := strings.CutSuffix(e.Name(), ".db")
			path := filepath.Join(dir, e.Name())
			if !ok || e.IsDir() || samePath(path, in.Logbook) || slices.ContainsFunc(others, func(c Caller) bool { return samePath(c.Logbook, path) }) {
				continue
			}
			cs, err := sessions(ctx, path, project)
			if err != nil {
				return Caller{}, err
			}
			others = append(others, cs...)
		}
	}
	if in.Cwd != "" {
		for _, o := range others {
			if o.Role == WorkerR && within(in.Cwd, o.dir, o.Repo) {
				in.Project, in.Logbook, in.Cwd = o.Project, o.Logbook, ""
				return Resolve(ctx, in)
			}
		}
	}
	c, ok, err := nearest(in.Proc, up, others)
	switch {
	case err != nil || !ok:
		return Caller{Name: Keeper, Role: KeeperR, How: ByNobody}, err
	case in.Project != "":
		c.Role = Foreign
	}
	return c, nil
}

// nearest is the session among cs whose recorded process is alive and the nearest of up.
func nearest(ps port.Proc, up []int, cs []Caller) (Caller, bool, error) {
	var found []Caller
	near := len(up)
	for _, c := range cs {
		i := slices.Index(up, c.Process.PID)
		if c.Process.PID <= 0 || i < 0 || i > near {
			continue
		}
		switch alive, err := ps.Alive(c.Process); {
		case err != nil:
			return Caller{}, false, exit(ExitCantCheck, bsname.Cmd+" doctor", err, bsname.Cmd+" can't tell whether %s's process runs", c.Name)
		case !alive:
		case i < near:
			near, found = i, []Caller{c}
		default:
			found = append(found, c)
		}
	}
	switch len(found) {
	case 0:
		return Caller{}, false, nil
	case 1:
		return found[0], true, nil
	}
	return Caller{}, false, claimed(found)
}

// claimed is the refusal for several sessions claiming one bs.
func claimed(cs []Caller) error {
	var names []string
	for _, c := range cs {
		names = append(names, c.Name+" ("+c.Logbook+")")
	}
	return exit(ExitFail, bsname.Cmd+" status, then "+bsname.Cmd+" end the one that shouldn't be there", nil, "this bs is claimed by %d live sessions: %s", len(cs), strings.Join(names, ", "))
}

// heal finds a worker of P recorded in the hinted pane with no live start (none yet, or
// its process died and was restarted by hand) whose harness, an argv it owns as the
// worker's, runs there above self. The old bs's workers are never healed (SPEC7).
func heal(ctx context.Context, in ResolveIn, up []int, mine []Caller) (Caller, bool, error) {
	if in.Pane == "" || in.Harness == nil {
		return Caller{}, false, nil
	}
	var probe *herdr.PaneProcesses // looked up once
	var found []Caller
	for _, c := range mine {
		if c.Role != WorkerR || c.Session != in.Session || c.Pane != in.Pane || c.legacy {
			continue
		}
		if c.Process.PID > 0 {
			if alive, err := in.Proc.Alive(c.Process); err != nil || alive {
				continue // a live start not above self is someone else's; one bs can't look at isn't healed
			}
		}
		if probe == nil {
			pi, err := (herdr.Client{Run: in.Run, Session: in.Session}).ProcessInfo(ctx, in.Pane)
			if err != nil && !errors.Is(err, herdr.ErrPaneGone) {
				return Caller{}, false, exit(ExitCantCheck, "herdr --session "+in.Session+" pane process-info --pane "+in.Pane, err, "pane %s holds %s, whose start was never confirmed, and "+bsname.Cmd+" can't look for its harness", in.Pane, c.Name)
			}
			probe = &pi
		}
		if slices.ContainsFunc(probe.Foreground, func(f herdr.PaneProcess) bool {
			h := in.Harness(c.harness)
			return h != nil && isOwn(f, h, program(c.program, h), c.Name) && slices.Contains(up, f.PID)
		}) {
			c.Unconfirmed = true
			found = append(found, c)
		}
	}
	switch len(found) {
	case 0:
		return Caller{}, false, nil
	case 1:
		return found[0], true, nil
	}
	return Caller{}, false, claimed(found)
}

// tripwire takes the hinted herdr pane, which no entry recorded, for the live worker of P
// whose recorded herdr workspace holds it, or whose workspace directory it works in.
func tripwire(ctx context.Context, in ResolveIn, mine []Caller) (Caller, bool, error) {
	if in.Pane == "" || !slices.ContainsFunc(mine, func(c Caller) bool { return c.Role == WorkerR }) {
		return Caller{}, false, nil
	}
	panes, err := (herdr.Client{Run: in.Run, Session: in.Session}).PaneDirs(ctx)
	if err != nil {
		return Caller{}, false, exit(ExitCantCheck, "herdr --session "+in.Session+" pane list", err, "no recorded session runs this bs, and "+bsname.Cmd+" can't tell whether its pane %s is in a worker's workspace", in.Pane)
	}
	i := slices.IndexFunc(panes, func(p herdr.PaneDir) bool { return p.ID == in.Pane })
	if i < 0 {
		return Caller{}, false, nil
	}
	p := panes[i]
	var found []Caller
	for _, c := range mine {
		if c.Role != WorkerR || c.Session != in.Session {
			continue
		}
		if (c.herdrWS != "" && p.Workspace == c.herdrWS) || within(p.Cwd, c.dir, c.Repo) || within(p.ForegroundCwd, c.dir, c.Repo) {
			c.How, c.Workspace = ByTripwire, c.herdrWS
			found = append(found, c)
		}
	}
	switch len(found) {
	case 0:
		return Caller{}, false, nil
	case 1:
		return found[0], true, nil
	}
	return Caller{}, false, claimed(found)
}

// within reports whether dir is ws or under it; a workspace that is the repo itself (kind
// none) holds every shell in the repo, so it counts for nothing. Both are compared with
// symlinks resolved: a cwd from the OS is the real path, a recorded dir may not be
// (macOS's /var is /private/var, bees-13.89).
func within(dir, ws, repo string) bool {
	if dir == "" || ws == "" || samePath(ws, repo) {
		return false
	}
	rel, err := filepath.Rel(resolved(ws), resolved(dir))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// sessions reads every open session of project's logbook at path, read-only.
func sessions(ctx context.Context, path, project string) ([]Caller, error) {
	cant := func(err error) error {
		return exit(ExitCantCheck, "check "+path+" (a logbook dir holds only <project>.db logbooks)", err, bsname.Cmd+" can't read logbook %s to find who runs it", path)
	}
	lb, err := logbook.OpenReadOnly(ctx, path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, cant(err)
	}
	defer lb.Close()
	recs, err := lb.Sessions(ctx)
	if err != nil {
		return nil, cant(err)
	}
	out := make([]Caller, 0, len(recs))
	for _, r := range recs {
		c := Caller{Name: r.Name, Role: WorkerR, Project: project, Spawn: r.Spawn, How: ByAncestor, Logbook: path,
			Session: r.Session, Pane: r.Pane, Repo: r.Repo, harness: r.Harness}
		if r.HasProcess {
			c.Process = r.Process
		}
		if r.Queen {
			c.Role = QueenR
		} else {
			sp, err := lb.Spawn(ctx, r.Spawn)
			if err != nil {
				return nil, cant(err)
			}
			c.Spawned, c.Repo, c.herdrWS, c.dir, c.legacy = sp.TS, sp.Workspace.Repo, sp.Herdr.Workspace, sp.Workspace.Dir, sp.Legacy()
			c.program = sp.Program
		}
		out = append(out, c)
	}
	return out, nil
}

// Confirm finishes w, the caller Resolve found, against this core's logbook (P's): it
// heals an unconfirmed worker's start (records it late), and warns of a tripwire.
func (c *Core) Confirm(ctx context.Context, w Caller) (Caller, error) {
	switch {
	case w.How == ByTripwire:
		c.warn("no recorded session runs this bs, but its herdr pane is in %s's workspace: taken for %s, with a worker's permissions", w.Name, w.Name)
	case w.Unconfirmed:
		sp, err := c.Logbook.Spawn(ctx, w.Spawn)
		if err != nil {
			return Caller{}, err
		}
		st, ok, err := c.StartOf(ctx, sp)
		switch {
		case err != nil:
			return Caller{}, exit(ExitCantCheck, "herdr --session "+w.Session+" pane process-info --pane "+w.Pane, err, "pane %s holds %s, whose start was never confirmed", w.Pane, w.Name)
		case !ok:
			return Caller{}, exit(ExitStart, bsname.Cmd+" end --incomplete "+w.Name, nil, "pane %s holds %s, whose start was never confirmed and whose harness "+bsname.Cmd+" can't find there", w.Pane, w.Name)
		}
		w.Process, w.Unconfirmed = st.Process, false
	}
	return w, nil
}

// As is w with --as as applied: a debug override, the keeper's alone, acting as the named
// session of P with its role (and an inbox read so only peeks). Under any recorded session
// --as must name that session (exit 2).
func (c *Core) As(ctx context.Context, w Caller, as string) (Caller, error) {
	switch {
	case as == "" || as == w.Name:
		return w, nil
	case w.Recorded():
		return Caller{}, exit(ExitRefused, "", nil, "--as %s: this bs runs under %s; --as is the keeper's, from a shell no session started", as, w.Name)
	case as == Keeper:
		return w, nil
	case as == Queen(c.Project):
		return Caller{Name: as, Role: QueenR, Project: c.Project, How: ByAs, Peek: true}, nil
	}
	sp, ok, err := c.Logbook.LatestSpawn(ctx, as)
	switch {
	case err != nil:
		return Caller{}, err
	case !ok || sp.Ended:
		return Caller{}, exit(ExitRefused, bsname.Cmd+" status", nil, "--as %s: no open spawn for %s in project %s", as, as, c.Project)
	}
	return Caller{Name: as, Role: WorkerR, Project: c.Project, Spawn: sp.ID, Spawned: sp.TS, How: ByAs, Peek: true}, nil
}

// Observe records what worker w's session tells bs about itself in env, the env of the bs
// it runs (port.Harness.Observe; agy's conversation id, which a resume continues): only
// for a worker found by its recorded ancestor, whose env is its session's own. A failed
// record warns and the command runs: a resume then refuses for want of it, saying so.
func (c *Core) Observe(ctx context.Context, w Caller, env []string) {
	if w.Role != WorkerR || w.How != ByAncestor {
		return
	}
	h := c.harnessOf(w.harness)
	if h == nil {
		return
	}
	if _, err := c.Logbook.RecordObserved(ctx, w.Spawn, w.Name, h.Observe(env)); err != nil {
		c.warn("couldn't record what %s's session says of itself (a resume needs it): %v", w.Name, err)
	}
}

// table is the permission table (ADR-0010 Amendment 4 Permissions), the one place it
// lives: the roles that may run each command. A worker's check and done are of itself; a
// foreign session's send goes to queen-P. A command not here runs for every role.
var table = map[string][]Role{
	"queen": {KeeperR}, "spawn": {KeeperR, QueenR}, "end": {KeeperR, QueenR}, "prune": {KeeperR, QueenR},
	"goal": {KeeperR, QueenR}, "goal:discard": {KeeperR},
	"check": {KeeperR, QueenR, WorkerR}, "done": {WorkerR},
	"send": {KeeperR, QueenR, WorkerR, Foreign}, "inbox": {KeeperR, QueenR, WorkerR},
	"bead:new": {KeeperR, QueenR, WorkerR}, "epic": {KeeperR, QueenR, WorkerR},
}

// doing is how a refusal names what each command does.
var doing = map[string]string{
	"queen": "starts a queen", "spawn": "spawns a worker", "end": "ends a worker", "prune": "prunes",
	"goal": "changes a goal", "goal:discard": "discards a goal", "check": "checks a worker",
	"done": "hands off", "inbox": "reads mail", "bead:new": "creates a bead", "epic": "creates an epic",
}

// May refuses w running cmd in project p (exit 2) unless the table lets its role; subject
// is the worker or session cmd acts on ("" when none).
func May(w Caller, cmd, p, subject string) error {
	roles, listed := table[cmd]
	ok := !listed || slices.Contains(roles, w.Role)
	switch {
	case ok && w.Role == WorkerR && cmd == "check" && subject != "" && subject != w.Name:
		return exit(ExitRefused, bsname.Cmd+" check "+w.Name, nil, "%s runs this as a worker; a worker checks only its own work", w.Name)
	case ok && w.Role == Foreign && cmd == "send" && subject != "" && subject != Queen(p): // refused below
	case ok:
		return nil
	}
	if w.Role == Foreign {
		return exit(ExitRefused, bsname.Cmd+" send "+Queen(p)+" \"...\"", nil, "you are %s of %s; message %s, the queen of %s, rather than %s", w.Name, w.Project, Queen(p), p, cmd)
	}
	var who []string
	for _, r := range []Role{QueenR, KeeperR, WorkerR} {
		if slices.Contains(roles, r) {
			who = append(who, article(r))
		}
	}
	why := "only " + strings.Join(who, " or ") + " " + doing[cmd]
	if cmd == "goal" && w.Role == WorkerR {
		why = "a worker never changes a goal; " + why
	}
	next := bsname.Cmd + " send " + Queen(p) + " \"...\""
	switch w.Role {
	case QueenR:
		next = "ask the keeper"
	case KeeperR:
		next = bsname.Cmd + " status"
	}
	return exit(ExitRefused, next, nil, "%s runs this as %s; %s", w.Name, article(w.Role), why)
}

// article is a role with its article.
func article(r Role) string {
	if r == WorkerR {
		return "a worker"
	}
	return "the " + string(r)
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
