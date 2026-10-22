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

	"github.com/jmmshn/bees/cmd/bsx/internal/config"
	"github.com/jmmshn/bees/cmd/bsx/internal/core"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// queen records the queen of the project in this pane and execs her harness, which keeps
// this process's pid and start time (ADR-0010). Her bs calls find her by that pane.
func (r *run) queen(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: bsx queen takes no arguments", errUsage)
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
		return &core.Exit{Code: core.ExitRefused, Msg: "no harness adapter for harness = " + r.s.Harness, Next: "bsx config"}
	}
	exe, ok := lookPath(core.Getenv(r.env.Vars, "PATH"), r.c.Harness.Kind())
	if !ok {
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("%s is not on PATH", r.c.Harness.Kind()), Next: "bsx doctor"}
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
	return r.ports.Exec(r.s.Repo, env, argv)
}

func (r *run) spawn(fs *flag.FlagSet, args []string) error {
	var goals list
	fs.Var(&goals, "goal", "what the worker hands off (repeatable)")
	label := fs.String("label", "", "its herdr label (default: the bead's title)")
	as := fs.String("as", "", "the queen, outside a herdr pane")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: bsx spawn --goal <goal> <bead>", errUsage)
	}
	if err := r.s.unpinned("spawn"); err != nil {
		return err
	}
	if r.c.Harness == nil {
		return &core.Exit{Code: core.ExitRefused, Msg: "no harness adapter for harness = " + r.s.Harness, Next: "bsx config"}
	}
	w, err := r.who(*as)
	if err != nil {
		return err
	}
	sp, err := r.c.Spawn(r.ctx, core.SpawnReq{By: w.Name, Session: cmp.Or(core.Getenv(r.env.Vars, "HERDR_SESSION"), logbook.DefaultSession),
		Bead: fs.Arg(0), Label: *label, Goals: goals})
	if err != nil {
		return err
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
	wait := fs.Bool("wait", false, "while CI is still running (or the host hasn't shown a push yet), wait for it (up to --timeout), then hand off or name what failed")
	timeout := fs.Duration("timeout", 8*time.Minute, "how long --wait waits (under the 10 min a harness's shell call may take)")
	as := fs.String("as", "", "the worker, outside a herdr pane")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: bsx done takes no arguments; say it with --report (got %q)", errUsage, strings.Join(fs.Args(), " "))
	}
	if *report == "-" {
		b, err := io.ReadAll(r.env.Stdin)
		if err != nil {
			return err
		}
		*report = strings.TrimSpace(string(b))
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
		if c.Detail != "" { // a vacuous CI pass among them: said, never silent (Rule G)
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
		return fmt.Errorf("%w: bsx check <worker>", errUsage)
	}
	name := core.Name(fs.Arg(0))
	rs, err := r.c.Check(r.ctx, name, r.env.Self)
	for _, c := range rs {
		mark := "ok  "
		switch {
		case c.Err != "":
			mark = "??  "
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
		return fmt.Errorf("%w: bsx goal <worker> <goal>... | discard | --waive <check>", errUsage)
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
	as := fs.String("as", "", "the queen, outside a herdr pane")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: bsx end <worker>", errUsage)
	}
	w, err := r.who(*as)
	if err != nil {
		return err
	}
	name := core.Name(fs.Arg(0))
	if err := r.c.End(r.ctx, w.Name, name, r.env.Self, core.EndOpts{Force: *force, Incomplete: *incomplete}); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "ended %s; its workspace stays for bsx prune\n", name)
	return nil
}

func (r *run) prune(fs *flag.FlagSet, args []string) error {
	as := fs.String("as", "", "the queen, outside a herdr pane")
	if err := fs.Parse(args); err != nil {
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
		if p.Remote != "" {
			fmt.Fprintf(r.out, "  left %s: bs never touches a remote branch; it is the keeper's\n", p.Remote)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("%w: bsx send [--project P] [--now] <to> <message | ->", errUsage)
	}
	w, err := r.who(*as)
	if err != nil {
		return err
	}
	to, body := fs.Arg(0), fs.Arg(1)
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
		// so the reply needs no project read back out of a name (motif 3).
		path := filepath.Join(filepath.Dir(r.s.Logbook), p+".db")
		if _, err := os.Stat(path); err != nil {
			return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("bsx send --project %s: no logbook at %s; nothing stored", p, path), Err: err,
				Next: "check the project name: bs bead ls names it, and its queen starts its logbook"}
		}
		lb, err := logbook.Open(r.ctx, path)
		if err != nil {
			return err
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
		fmt.Fprintf(r.err, "bsx send: warning: %s is neither %s nor a worker spawned in project %s: stored, not rung; it reads it only as a session of %s\n",
			to, core.Queen(c.Project), c.Project, c.Project)
		return nil
	}
	priority := port.Next
	if *now {
		priority = port.Now
	}
	r.ring(c, w.Name, to, priority)
	return nil
}

// ring rings to and says how it went. The message is already stored, so no outcome fails
// the command: the logbook entry is the delivery, the ring only when it is read (motif 10).
func (r *run) ring(c *core.Core, from, to string, p port.Priority) {
	switch err := c.Ring(r.ctx, from, to, p); {
	case err == nil:
		fmt.Fprintf(r.out, "rung %s\n", to)
	case errors.Is(err, port.ErrNotRunning):
		fmt.Fprintf(r.out, "queued (%v): %s reads it at its next bs inbox\n", err, to)
	case errors.Is(err, port.ErrNoRing):
		fmt.Fprintf(r.out, "stored; %s's harness has no ring, so it reads this at its next bs inbox\n", to)
	default:
		fmt.Fprintf(r.err, "bsx %s: couldn't ring %s (the message is stored): %v\n", r.name, to, err)
	}
}

func (r *run) inbox(fs *flag.FlagSet, args []string) error {
	as := fs.String("as", "", "the reader, outside a recorded herdr pane")
	if err := fs.Parse(args); err != nil {
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
			fmt.Fprintf(r.out, "   (reply with: bs send --project %s %s \"...\")\n\n", m.FromProject, m.From)
		}
		ids = append(ids, m.ID)
	}
	return r.c.Logbook.RecordReceipts(r.ctx, w.Name, ids)
}

// status lists each worker's latest spawn: its goal, whether it ended, its handoff,
// whether its goal is complete (landed, and by what), and whether its recorded process
// runs; then a stuck: line with the next command for each worker that can't move on its
// own (Rule G). It reads the record and the process table, never a check: a goal
// complete but not yet landed shows "no" until bs end or bs prune finds it. A spawn from
// before goals reads --goal legacy.
func (r *run) status(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	sps, err := r.c.Logbook.LatestSpawns(r.ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(r.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKER\tGOAL\tSTATE\tHANDOFF\tCOMPLETE\tRUNNING\tWORKSPACE")
	var errs []error
	var stuck []string
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
		handoff := "-"
		if d, ok, err := r.c.Logbook.LatestDone(r.ctx, sp.ID); err != nil {
			errs = append(errs, err)
			handoff = "unknown"
		} else if ok {
			handoff = "done " + d.TS.Local().Format("01-02 15:04")
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
		if e, err := r.c.Logbook.Goal(r.ctx, sp); err != nil { // the newest amend, not the spawn's goal
			errs = append(errs, err)
		} else {
			g, discarded = e.String(), e.Discarded
			if e.Discarded {
				g += " (discarded)"
			}
		}
		dir := sp.Workspace.Dir
		fmt.Fprintf(tw, "%s\t--goal %s\t%s\t%s\t%s\t%s\t%s\n", sp.Name, g, state, handoff, complete, running, dir)
		switch {
		case lerr != nil: // couldn't read whether it landed: never read as "no" (Rule G)
		case running == "unconfirmed":
			stuck = append(stuck, fmt.Sprintf("%s: its start was never confirmed → bs end %s --incomplete", sp.Name, sp.Name))
		case strings.HasPrefix(running, "no"):
			next := "bs end " + sp.Name
			if !landed {
				next += " (--incomplete if its goal isn't complete)"
			}
			stuck = append(stuck, fmt.Sprintf("%s: its process is gone but it isn't ended → %s", sp.Name, next))
		case sp.Ended && !landed && !sp.Legacy() && !discarded:
			stuck = append(stuck, fmt.Sprintf("%s: ended with its goal incomplete → bs prune completes it once --goal %s holds", sp.Name, g))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, s := range stuck {
		fmt.Fprintln(r.out, "stuck: "+s)
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
