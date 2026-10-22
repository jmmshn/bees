package cli

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
)

// queen records the queen of the project in this pane and execs her harness, which keeps
// this process's pid and start time (ADR-0010). Her bs calls find her by that pane.
func (r *run) queen(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: "+bsname.Cmd+" queen takes no arguments", errUsage)
	}
	pane := core.Getenv(r.env.Vars, "HERDR_PANE_ID")
	if pane == "" { // before anything else bs queen needs: no pane, no queen
		return &core.Exit{Code: core.ExitRefused, Msg: "not in a herdr pane (HERDR_PANE_ID is unset): start the queen in a herdr pane, so her workers can find and ring her"}
	}
	if err := r.s.unpinned("queen"); err != nil {
		return err
	}
	if p := r.s.Pane; p != nil {
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("this pane runs %s already", p.Who.Name), Next: "start the queen in a pane of her own"}
	}
	if r.c.Harness == nil {
		return &core.Exit{Code: core.ExitRefused, Msg: "no harness adapter for harness = " + r.s.Harness, Next: bsname.Cmd + " config"}
	}
	exe, ok := lookPath(core.Getenv(r.env.Vars, "PATH"), r.c.Harness.Kind())
	if !ok {
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("%s is not on PATH", r.c.Harness.Kind()), Next: bsname.Cmd + " doctor"}
	}
	if err := r.c.CheckGuards(r.ctx); err != nil { // bees-12.20: refused now, not at every spawn
		return err
	}
	argv, err := r.c.Queen(r.ctx, core.QueenReq{Session: r.s.Session, Pane: pane, Self: r.env.Self, Exe: exe})
	if err != nil {
		return err
	}
	// bs sets no BS_* in a session's env (ADR-0010): her bs finds her by her pane.
	env := slices.DeleteFunc(slices.Clone(r.env.Vars), func(kv string) bool { return strings.HasPrefix(kv, "BS_") })
	if err := r.c.Logbook.Close(); err != nil { // exec keeps no logbook open
		return err
	}
	argv[0] = exe
	fmt.Fprintf(r.out, "%s recorded in pane %s on herdr session %s; starting %s\n", core.Queen(r.s.Project), core.Getenv(r.env.Vars, "HERDR_PANE_ID"), r.s.Session, exe)
	if err := r.ports.Exec(r.s.Repo, env, argv); err != nil {
		return &core.Exit{Code: core.ExitFail, Msg: fmt.Sprintf("couldn't start %s (her entry names this pid, which exits: harmless)", exe), Err: err, Next: bsname.Cmd + " doctor"}
	}
	return nil
}

// spawnArgs is bs spawn's arguments.
type spawnArgs struct {
	goals     list
	label, as string
	bead      string
}

// spawnArgs parses bs spawn's arguments and, when the project has no logbook yet, checks
// its bead exists, before the logbook is opened or made (bees-13.9 review S3).
func (r *run) spawnArgs(fs *flag.FlagSet, args []string) error {
	a := &r.spawnReq
	fs.Var(&a.goals, "goal", "what the worker hands off (repeatable)")
	fs.StringVar(&a.label, "label", "", "its herdr label (default: the bead's title)")
	fs.StringVar(&a.as, "as", "", "the queen, outside a recorded herdr pane")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: "+bsname.Cmd+" spawn --goal <goal> <bead>", errUsage)
	}
	a.bead = fs.Arg(0)
	if _, err := goal.Parse(a.goals); err != nil { // Spawn says the same; here before a logbook is made
		return &core.Exit{Code: core.ExitRefused, Msg: a.bead, Err: err}
	}
	if _, err := os.Stat(r.s.Logbook); errors.Is(err, os.ErrNotExist) && r.ports.Beads != nil {
		if _, err := r.ports.Beads.Show(r.ctx, a.bead); errors.Is(err, port.ErrNoBead) {
			return &core.Exit{Code: core.ExitRefused, Msg: "no bead " + a.bead + "; no logbook was made", Next: "bd show " + a.bead}
		} else if err != nil {
			return &core.Exit{Code: core.ExitCantCheck, Msg: "can't read bead " + a.bead + "; no logbook was made", Err: err}
		}
	}
	return nil
}

func (r *run) spawn(_ *flag.FlagSet, _ []string) error {
	a := r.spawnReq
	goals, label, as := a.goals, &a.label, &a.as
	if err := r.s.unpinned("spawn"); err != nil {
		return err
	}
	if r.c.Harness == nil {
		return &core.Exit{Code: core.ExitRefused, Msg: "no harness adapter for harness = " + r.s.Harness, Next: bsname.Cmd + " config"}
	}
	// A kind the config chose and spawn can't make yet is refused, never made a worktree;
	// a worked-out none is left to Spawn, which names why the repo isn't a checkout.
	if r.s.RepoMissing {
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("the repo %s (%s) doesn't exist", r.s.Repo, r.s.RepoFrom), Next: "set paths.repo to the project's checkout"}
	}
	if kind, why, chosen := r.s.Kind(r.s.GitTop); chosen && unmade(kind) != "" {
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("workspace kind %s (%s): %s", kind, why, unmade(kind))}
	}
	w, err := r.who(*as)
	if err != nil {
		return err
	}
	sp, resumed, err := r.c.Spawn(r.ctx, core.SpawnReq{By: w.Name, Session: cmp.Or(core.Getenv(r.env.Vars, "HERDR_SESSION"), logbook.DefaultSession),
		Bead: a.bead, Label: *label, Goals: goals})
	if err != nil {
		return err
	}
	if resumed {
		fmt.Fprintf(r.out, "resumed %s in pane %s on herdr session %s, in %s\n", sp.Name, sp.Herdr.Pane, sp.Herdr.Session, sp.Workspace.Dir)
		return nil
	}
	fmt.Fprintf(r.out, "spawned %s (--goal %s) in %s, pane %s on herdr session %s\n", sp.Name, sp.Goal, sp.Workspace.Dir, sp.Herdr.Pane, sp.Herdr.Session)
	return nil
}

func (r *run) done(fs *flag.FlagSet, args []string) error {
	var prs list
	fs.Var(&prs, "pr", "a PR you hand off (repeatable)")
	var replaces list
	fs.Var(&replaces, "replaces", "a PR you handed off before that this handoff replaces (repeatable)")
	answer := fs.String("answer", "", "a verdict goal's answer: yes or no")
	report := fs.String("report", "", "a verdict goal's report, or - to read it from stdin")
	force := fs.Bool("force", false, "hand off while background work runs (waives that gate only)")
	wait := fs.Bool("wait", r.name == "handoff", "while CI is still running (or the host hasn't shown a push yet), wait for it (up to --timeout), then hand off or name what failed")
	timeout := fs.Duration("timeout", 8*time.Minute, "how long --wait waits, at most 9m (bs gives a worker's shell calls 10)")
	as := fs.String("as", "", "the worker, outside a recorded herdr pane")
	project := fs.String("project", "", "refused unless it names your own project: a handoff goes to your own queen")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: "+bsname.Cmd+" done takes no arguments; say it with --report (got %q)", errUsage, strings.Join(fs.Args(), " "))
	}
	if err := r.ownProject("done", "handoff", *project); err != nil {
		return err
	}
	if *timeout > core.MaxWait {
		return fmt.Errorf("%w: --timeout %s: at most %s, so %s answers before the worker's %s shell call is cut", errUsage, *timeout, core.MaxWait, bsname.Cmd, core.ShellTimeout)
	}
	if *report == "-" {
		b, err := io.ReadAll(r.env.Stdin)
		if err != nil {
			return err
		}
		if *report = strings.TrimSpace(string(b)); *report == "" {
			return fmt.Errorf("%w: %s done --report -: stdin was empty", errUsage, bsname.Cmd)
		}
	}
	w, err := r.who(*as)
	if err != nil {
		return err
	}
	d, err := r.c.Done(r.ctx, w, r.env.Self, core.Handed{PRs: prs, Replaces: replaces, Answer: *answer, Report: *report}, core.DoneOpts{Force: *force, Wait: waitFor(*wait, *timeout)})
	if err != nil {
		return err
	}
	fmt.Fprintf(r.out, "handed off to %s:\n%s\n", d.Queen, d.Text())
	for _, c := range d.Checks {
		if c.Detail != "" && !c.SaysWhere() { // a vacuous CI pass among them: said, never silent (Rule G); Text has the rest
			fmt.Fprintf(r.out, "  %s: %s\n", c.Name, c.Detail)
		}
	}
	r.ring(r.c, d.Name, d.Queen, port.Next)
	return nil
}

// check runs a worker's handoff checks now and prints each result; it writes nothing.
func (r *run) check(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: "+bsname.Cmd+" check <worker>", errUsage)
	}
	name := core.Name(fs.Arg(0))
	rs, err := r.c.Check(r.ctx, name, r.env.Self)
	for _, c := range rs {
		mark := "ok  "
		switch {
		case c.Err != "":
			mark = "??  "
		case c.Pending:
			mark = "WAIT"
		case !c.Pass:
			mark = "FAIL"
		}
		line := mark + " " + c.Name
		if d := c.Detail + c.Err; d != "" {
			line += ": " + d
		}
		fmt.Fprintln(r.out, line)
	}
	return err
}

// goal amends, waives or discards a worker's goal.
func (r *run) goal(fs *flag.FlagSet, args []string) error {
	waive := fs.String("waive", "", "a check of its goal no longer run")
	as := fs.String("as", "", "the queen, outside a herdr pane (default: the keeper)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("%w: "+bsname.Cmd+" goal <worker> <goal>... | discard | --waive <check>", errUsage)
	}
	name := core.Name(fs.Arg(0))
	// ADR-0010 spells it bs goal <name> --waive <check>: flags after the worker too.
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return err
	}
	by := cmp.Or(*as, "keeper")
	if r.s.Pane != nil {
		w, err := r.who(*as)
		if err != nil {
			return err
		}
		by = w.Name
	}
	e, err := r.c.Goal(r.ctx, core.GoalReq{By: by, Self: r.env.Self, Name: name, Values: fs.Args(), Waive: *waive})
	if err != nil {
		return err
	}
	g := "--goal " + e.String()
	if e.Discarded {
		g += " (discarded)"
	}
	if len(e.Waived) > 0 {
		g += " (waived: " + strings.Join(e.Waived, ", ") + ")"
	}
	fmt.Fprintf(r.out, "%s: %s\n", name, g)
	return nil
}

// waitFor is how long bs done waits for CI: --timeout with --wait, else not at all.
func waitFor(wait bool, timeout time.Duration) time.Duration {
	if !wait {
		return 0
	}
	return max(timeout, time.Second)
}

func (r *run) end(fs *flag.FlagSet, args []string) error {
	force := fs.Bool("force", false, "end while background work runs (waives that gate only)")
	incomplete := fs.Bool("incomplete", false, "end an incomplete goal; prune completes it later")
	as := fs.String("as", "", "the queen, outside a recorded herdr pane")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: "+bsname.Cmd+" end <worker>", errUsage)
	}
	w, err := r.who(*as)
	if err != nil {
		return err
	}
	name := core.Name(fs.Arg(0))
	if err := r.c.End(r.ctx, w.Name, name, r.env.Self, core.EndOpts{Force: *force, Incomplete: *incomplete}); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "ended %s; its workspace stays for "+bsname.Cmd+" prune\n", name)
	return nil
}

func (r *run) prune(fs *flag.FlagSet, args []string) error {
	as := fs.String("as", "", "the queen, outside a recorded herdr pane")
	if err := parse(fs, args); err != nil {
		return err
	}
	w, err := r.who(*as)
	if err != nil {
		return err
	}
	ps, err := r.c.Prune(r.ctx, w.Name)
	if err != nil {
		return err
	}
	code := 0
	for _, p := range ps {
		var did []string
		if p.Removed {
			did = append(did, p.Dir)
		}
		if p.Deleted {
			did = append(did, "branch "+p.Branch)
		}
		if len(did) > 0 {
			fmt.Fprintf(r.out, "removed %s (%s)\n", strings.Join(did, " and "), p.Name)
		}
		if p.Deleted && p.Held != "" {
			fmt.Fprintf(r.out, "  its work is on %s\n", p.Held)
		}
		if p.Remote != "" {
			fmt.Fprintf(r.out, "  left %s: %s never touches a remote branch; it is the keeper's\n", p.Remote, bsname.Cmd)
		}
		what := p.Dir
		if p.Removed || p.Gone {
			what = "branch " + p.Branch
		}
		switch {
		case p.Why == "":
		case p.Unknown:
			fmt.Fprintf(r.out, "kept %s (%s): couldn't check: %s\n", what, p.Name, p.Why)
			code = core.ExitUnknown
		default:
			fmt.Fprintf(r.out, "kept %s (%s): %s\n", what, p.Name, p.Why)
			code = max(code, core.ExitKept)
		}
	}
	if code != 0 {
		return &core.Exit{Code: code, Msg: "kept some workspaces"}
	}
	return nil
}

func (r *run) send(fs *flag.FlagSet, args []string) error {
	as := fs.String("as", "", "the sender, outside a recorded herdr pane")
	project := fs.String("project", "", "write into another project's logbook (its session reads it there)")
	now := fs.Bool("now", false, "ring with priority now: interrupt the recipient's current turn")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("%w: "+bsname.Cmd+" send [--project P] [--now] <to> <message | ->", errUsage)
	}
	w, err := r.who(*as)
	if err != nil {
		return err
	}
	to, body := core.Name(fs.Arg(0)), fs.Arg(1) // a bead id names its worker (ADR-0006)
	if body == "-" {
		b, err := io.ReadAll(r.env.Stdin)
		if err != nil {
			return err
		}
		body = strings.TrimRight(string(b), "\n")
	}
	c, fromProject := r.c, ""
	if p := config.Normalise(*project); p != "" && p != r.s.Project {
		// Cross-project mail (T11): written into p's logbook, recording the sender's project
		// so the reply needs no project read back out of a name (motif 3). Only the
		// recipient's logbook changes: the sender stays who it is in its own project.
		path := filepath.Join(filepath.Dir(r.s.Logbook), p+".db")
		// The other logbook gets the same old-session refusal, before Open migrates it.
		if what, err := oldSessionsAt(r.ctx, path); err != nil {
			return &core.Exit{Code: core.ExitCantCheck, Msg: fmt.Sprintf(bsname.Cmd+" send --project %s: can't read %s; nothing stored", p, path), Err: err, Next: bsname.Cmd + " doctor"}
		} else if what != "" {
			return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("%s send --project %s: %s; nothing stored", bsname.Cmd, p, what),
				Next: "its queen or keeper ends them first: " + bsname.Cmd + " end --as " + core.Queen(p) + " <worker>, in " + p + "'s repo"}
		}
		lb, err := logbook.Open(r.ctx, path)
		if errors.Is(err, logbook.ErrNoLogbook) {
			return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf(bsname.Cmd+" send --project %s: no logbook at %s; nothing stored", p, path),
				Next: "check the project name against the logbooks: ls " + filepath.Dir(path)}
		} else if err != nil {
			return &core.Exit{Code: core.ExitCantCheck, Msg: fmt.Sprintf(bsname.Cmd+" send --project %s: can't read %s; nothing stored", p, path), Err: err, Next: bsname.Cmd + " doctor"}
		}
		defer lb.Close()
		cc := *r.c
		cc.Logbook, cc.Project = lb, p
		c, fromProject = &cc, r.s.Project
	}
	stranger, err := c.Addressed(r.ctx, to, filepath.Dir(r.s.Logbook))
	if err != nil {
		return err
	}
	id, err := c.Logbook.SendFrom(r.ctx, w.Name, fromProject, to, body)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.out, "sent %s to %s\n", id, to)
	if stranger {
		fmt.Fprintf(r.err, bsname.Cmd+" send: warning: %s is neither %s nor a worker spawned in project %s: stored, not rung; it reads it only as a session of %s, and a worker spawned as %s later reads only mail sent after its spawn\n",
			to, core.Queen(c.Project), c.Project, c.Project, to)
		return nil
	}
	priority := port.Next
	if *now {
		priority = port.Now
	}
	r.ring(c, w.Name, to, priority)
	return nil
}

// ownProject refuses cmd --project naming (what: the noun for what cmd reads or writes) another project than the caller's (T11): only
// bs send writes across projects. A session's mail and handoff are in its own project's
// logbook, and inside a recorded pane that project is the pane's.
func (r *run) ownProject(cmd, what, project string) error {
	p := config.Normalise(project)
	if p == "" || p == r.s.Project {
		return nil
	}
	if r.s.Pane != nil {
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("%s %s --project %s: you are %s of project %s, so your %s is in %s's logbook; only %s send crosses projects",
			bsname.Cmd, cmd, p, r.s.Pane.Who.Name, r.s.Project, what, r.s.Project, bsname.Cmd), Next: bsname.Cmd + " " + cmd + " (no --project)"}
	}
	return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("%s %s --project %s: this shell is in project %s (%s); only %s send crosses projects", bsname.Cmd, cmd, p, r.s.Project, r.s.ProjectFrom, bsname.Cmd),
		Next: "run it from " + p + "'s repo"}
}

// ring rings to and says how it went. The message is already stored, so no outcome fails
// the command: the logbook entry is the delivery, the ring only when it is read (motif 10).
func (r *run) ring(c *core.Core, from, to string, p port.Priority) {
	switch err := c.Ring(r.ctx, from, to, p); {
	case err == nil:
		fmt.Fprintf(r.out, "rung %s\n", to)
	case errors.Is(err, port.ErrNotRunning), errors.Is(err, port.ErrNoRing):
		fmt.Fprintf(r.out, "stored, not rung (%v): %s reads it at its next "+bsname.Cmd+" inbox\n", err, to)
	case errors.Is(err, core.ErrEnded):
		fmt.Fprintf(r.out, "stored, not rung: %v, so nobody reads it (a later spawn reads only mail sent after it)\n", err)
	default:
		fmt.Fprintf(r.err, bsname.Cmd+" %s: couldn't ring %s (the message is stored): %v\n", r.name, to, err)
	}
}

func (r *run) inbox(fs *flag.FlagSet, args []string) error {
	as := fs.String("as", "", "the reader, outside a recorded herdr pane")
	project := fs.String("project", "", "refused unless it names your own project: your mail is in your own project's logbook")
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := r.ownProject("inbox", "mail", *project); err != nil {
		return err
	}
	w, err := r.who(*as)
	if err != nil {
		return err
	}
	ms, err := r.c.Logbook.Unread(r.ctx, w.Name)
	if err != nil {
		return err
	}
	if len(ms) == 0 {
		fmt.Fprintln(r.out, "no unread messages")
		return nil
	}
	var ids []logbook.ID
	for _, m := range ms {
		fmt.Fprintf(r.out, "── %s from %s · %s\n%s\n\n", m.ID, m.From, m.TS.Local().Format("2006-01-02 15:04:05"), m.Body)
		if m.FromProject != "" { // cross-project mail: the sender reads its own project's logbook
			fmt.Fprintf(r.out, "   (reply with: "+bsname.Cmd+" send --project %s %s \"...\")\n\n", m.FromProject, m.From)
		}
		ids = append(ids, m.ID)
	}
	return r.c.Logbook.RecordReceipts(r.ctx, w.Name, ids)
}

// status lists each worker's latest spawn: its goal, whether it ended, its handoff,
// whether its goal is complete (landed, and by what), and whether its recorded process
// runs. Then one line per worker the table alone would leave a question about, each with
// the next command (Rule G): stuck (its process is gone, its start unconfirmed, or it
// ended incomplete), kept (complete or discarded, its workspace still on disk), waived,
// discarded, and late (a start healed after confirmation missed it). It reads the
// record, the process table and the disk, never a check, and asks herdr only to heal an
// unconfirmed start (core.StartOf), so with herdr down that start reads unknown and the
// rest still prints: a goal complete but not yet landed shows "no" until bs end or bs
// prune finds it. A spawn from before goals reads --goal legacy.
func (r *run) status(fs *flag.FlagSet, args []string) error {
	if err := parse(fs, args); err != nil {
		return err
	}
	sps, err := r.c.Logbook.LatestSpawns(r.ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(r.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKER\tGOAL\tSTATE\tHANDOFF\tCOMPLETE\tRUNNING\tWORKSPACE")
	var errs []error
	var notes []string
	note := func(kind, name, format string, a ...any) {
		notes = append(notes, kind+": "+name+": "+fmt.Sprintf(format, a...))
	}
	queen := core.Queen(r.s.Project)
	if q, ok, err := r.c.Logbook.LatestQueen(r.ctx, queen); err != nil {
		errs = append(errs, err)
	} else if ok {
		fmt.Fprintf(tw, "%s\t-\tqueen\t-\t-\t%s\tpane %s on herdr session %s\n", queen, alive(r.c.Proc.Alive(q.Process)), q.Pane, q.Session)
	}
	for _, sp := range sps {
		state := "live"
		if sp.Ended {
			state = "ended"
			if e, _, err := r.c.Logbook.EndOf(r.ctx, sp.ID); err != nil {
				errs = append(errs, err)
			} else if e.Incomplete {
				state += " --incomplete"
			}
		}
		handoff, handed := "-", false
		d, ok, derr := r.c.Logbook.LatestDone(r.ctx, sp.ID)
		if derr != nil {
			errs = append(errs, derr)
			handoff = "unknown"
		} else if ok {
			handoff, handed = "done "+d.TS.Local().Format("01-02 15:04"), d.Passed()
		}
		complete := "no"
		ld, landed, lerr := r.c.Logbook.LandedOf(r.ctx, sp.ID)
		switch {
		case lerr != nil:
			errs = append(errs, lerr)
			complete = "unknown"
		case landed:
			complete = "yes (" + ld.By + ")"
		case sp.Legacy():
			complete = "-"
		}
		running := "-"
		if !sp.Ended {
			running = r.running(sp)
		}
		g, discarded := sp.Goal.String(), false
		e, gerr := r.c.Logbook.Goal(r.ctx, sp) // the newest amend, not the spawn's goal
		if gerr != nil {
			errs = append(errs, gerr)
		} else {
			g, discarded = e.String(), e.Discarded
			if e.Discarded {
				g += " (discarded)"
			}
		}
		dir := sp.Workspace.Dir
		var serr error // the workspace on disk: nil, gone, or couldn't look
		if dir != "" {
			_, serr = os.Stat(dir)
		}
		if sp.Ended && dir != "" && errors.Is(serr, os.ErrNotExist) {
			dir += " (removed)"
		}
		fmt.Fprintf(tw, "%s\t--goal %s\t%s\t%s\t%s\t%s\t%s\n", sp.Name, g, state, handoff, complete, running, dir)
		switch {
		case running == "no start recorded (legacy)":
			note("stuck", sp.Name, "started by the old bs, which recorded no process → %s end %s", bsname.Cmd, sp.Name)
		case running == "unconfirmed":
			note("stuck", sp.Name, "its start was never confirmed → %s end %s --incomplete", bsname.Cmd, sp.Name)
		case strings.HasPrefix(running, "no"):
			next := bsname.Cmd + " end " + sp.Name
			if !landed {
				next = bsname.Cmd + " end --incomplete " + sp.Name + " if its goal isn't complete"
			}
			if discarded || gerr != nil { // nothing to resume for: only the end
				note("stuck", sp.Name, "its process is gone but it isn't ended → %s", next)
			} else {
				var flags []string
				for _, p := range e.Parts {
					flags = append(flags, "--goal "+p.String())
				}
				note("stuck", sp.Name, "its process is gone but it isn't ended → %s spawn %s %s resumes it in its pane, or %s", bsname.Cmd, strings.Join(flags, " "), sp.Bead, next)
			}
		case lerr != nil || gerr != nil || derr != nil: // couldn't read it: never read as "no" (Rule G)
		case sp.Ended && !landed && !sp.Legacy() && !discarded && !handed:
			// Completion needs a passing handoff, and an ended worker hands nothing off.
			note("stuck", sp.Name, "ended with nothing handed off, so its goal can't complete → the keeper discards it (%s goal %s discard, bees-13.6)", bsname.Cmd, sp.Name)
		case sp.Ended && !landed && !sp.Legacy() && !discarded:
			note("stuck", sp.Name, "ended with its goal incomplete → %s prune completes it once --goal %s holds", bsname.Cmd, g)
		case sp.Ended && (landed || discarded) && sp.Workspace.Kind == "git_worktree" && sp.Workspace.Dir != "":
			why := "its goal is complete"
			if discarded {
				why = "its goal is discarded"
			}
			if serr == nil {
				note("kept", sp.Name, "%s, but its workspace is still on disk → %s prune removes it, or names what keeps it", why, bsname.Cmd)
			} else if !errors.Is(serr, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("%s: can't tell whether its workspace is still on disk: %w", sp.Name, serr))
			}
		}
		if gerr == nil && !landed {
			if len(e.Waived) > 0 {
				note("waived", sp.Name, "%s no longer checked → %s goal %s %s puts it back", strings.Join(e.Waived, ", "), bsname.Cmd, sp.Name, e.String())
			}
			if discarded && !sp.Ended {
				note("discarded", sp.Name, "the keeper discarded its goal: the work is wanted nowhere → %s end %s", bsname.Cmd, sp.Name)
			}
		}
		if st, ok, err := r.c.Logbook.LatestStart(r.ctx, sp.ID); err != nil {
			errs = append(errs, err)
		} else if ok && st.Late && !sp.Ended {
			note("late", sp.Name, "its start was recorded late, after confirmation missed it (pid %d) → nothing to do; %s end %s if it misbehaves", st.PID, bsname.Cmd, sp.Name)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, n := range notes {
		fmt.Fprintln(r.out, n)
	}
	return errors.Join(errs...)
}

// running is whether sp's recorded process runs: yes, no, unconfirmed, or why bs
// couldn't look ("couldn't look" is never "no"). A start healed after confirmation
// missed it reads "(late)". A failed heal probe is printed and writes nothing.
func (r *run) running(sp logbook.Spawn) string {
	st, ok, err := r.c.StartOf(r.ctx, sp)
	switch {
	case err != nil:
		return "unknown: " + err.Error()
	case !ok && sp.Legacy():
		return "no start recorded (legacy)"
	case !ok:
		return "unconfirmed"
	}
	s := alive(r.c.Proc.Alive(st.Process))
	if st.Late {
		s += " (late)"
	}
	return s
}

// alive is a liveness lookup as status shows it.
func alive(up bool, err error) string {
	switch {
	case err != nil:
		return "unknown: " + err.Error()
	case up:
		return "yes"
	}
	return "no"
}
