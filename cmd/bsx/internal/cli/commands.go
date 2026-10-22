package cli

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"github.com/jmmshn/bees/cmd/bsx/internal/bsname"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/jmmshn/bees/cmd/bsx/internal/core"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

func (r *run) spawn(fs *flag.FlagSet, args []string) error {
	var goals list
	fs.Var(&goals, "goal", "what the worker hands off (repeatable)")
	label := fs.String("label", "", "its herdr label (default: the bead's title)")
	as := fs.String("as", "", "the queen, outside a herdr pane")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: bsx spawn --goal <goal> <bead>", errUsage)
	}
	if r.c.Harness == nil {
		return &core.Exit{Code: core.ExitRefused, Msg: "bsx spawn: no harness adapter yet (claude_code arrives with bees-13.3)"}
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
	answer := fs.String("answer", "", "a verdict goal's answer: yes or no")
	report := fs.String("report", "", "a verdict goal's report, or - to read it from stdin")
	force := fs.Bool("force", false, "hand off while background work runs (waives that gate only)")
	as := fs.String("as", "", "the worker, outside a herdr pane")
	if err := parse(fs, args); err != nil {
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
	d, err := r.c.Done(r.ctx, w, r.env.Self, core.Handed{PRs: prs, Answer: *answer, Report: *report}, *force)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.out, "handed off to %s:\n%s\n", d.Queen, d.Text())
	r.ring(d.Queen, "["+bsname.Cmd+"] 1 unread ("+d.Name+") — run: "+bsname.Cmd+" inbox")
	return nil
}

func (r *run) end(fs *flag.FlagSet, args []string) error {
	force := fs.Bool("force", false, "end while background work runs (waives that gate only)")
	incomplete := fs.Bool("incomplete", false, "end an incomplete goal; prune completes it later")
	as := fs.String("as", "", "the queen, outside a herdr pane")
	if err := parse(fs, args); err != nil {
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
		switch {
		case p.Removed && p.Why != "":
			fmt.Fprintf(r.out, "removed %s (%s); %s\n", p.Dir, p.Name, p.Why)
			code = max(code, core.ExitKept)
		case p.Removed && p.Branch != "":
			fmt.Fprintf(r.out, "removed %s (%s) and its branch %s\n", p.Dir, p.Name, p.Branch)
		case p.Removed:
			fmt.Fprintf(r.out, "removed %s (%s)\n", p.Dir, p.Name)
		case p.Unknown:
			fmt.Fprintf(r.out, "kept %s (%s): couldn't check: %s\n", p.Dir, p.Name, p.Why)
			code = core.ExitUnknown
		default:
			fmt.Fprintf(r.out, "kept %s (%s): %s\n", p.Dir, p.Name, p.Why)
			code = max(code, core.ExitKept)
		}
	}
	if code != 0 {
		return &core.Exit{Code: code, Msg: "kept some workspaces"}
	}
	return nil
}

func (r *run) send(fs *flag.FlagSet, args []string) error {
	as := fs.String("as", "", "the sender, outside a herdr pane")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("%w: bsx send <to> <message>", errUsage)
	}
	w, err := r.who(*as)
	if err != nil {
		return err
	}
	to := core.Name(fs.Arg(0)) // a bead id names its worker (ADR-0006)
	if _, err := r.c.Logbook.Send(r.ctx, w.Name, to, fs.Arg(1)); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "sent to %s\n", to)
	r.ring(to, "["+bsname.Cmd+"] 1 unread ("+w.Name+") — run: "+bsname.Cmd+" inbox")
	return nil
}

// ring rings name if its recorded process runs; a ring that can't land says so and the
// message waits for the next bs inbox (motif 10: the logbook entry is the delivery).
func (r *run) ring(name, text string) {
	sp, ok, err := r.c.Logbook.LatestSpawn(r.ctx, name)
	if err != nil || !ok || sp.Ended {
		return // a queen's ring arrives with bees-13.4
	}
	st, ok, err := r.c.Logbook.LatestStart(r.ctx, sp.ID)
	if err != nil || !ok {
		fmt.Fprintf(r.err, "bsx: %s has no confirmed start: not rung; the message waits for its "+bsname.Cmd+" inbox\n", name)
		return
	}
	alive, err := r.c.Proc.Alive(st.Process)
	if err == nil && !alive {
		err = port.ErrNotRunning
	}
	if err == nil && r.c.Harness == nil { // bsx's own main until bees-13.3's adapter
		err = port.ErrNoRing
	}
	if err == nil {
		err = r.c.Harness.Ring(port.Target{Name: name, Session: sp.Herdr.Session, Pane: sp.Herdr.Pane, PID: st.PID}, text, port.Next)
	}
	if err != nil {
		fmt.Fprintf(r.err, "bsx: not rung (%v); the message waits for %s's "+bsname.Cmd+" inbox\n", err, name)
	}
}

func (r *run) inbox(fs *flag.FlagSet, args []string) error {
	as := fs.String("as", "", "the reader, outside a herdr pane")
	if err := parse(fs, args); err != nil {
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
		ids = append(ids, m.ID)
	}
	return r.c.Logbook.RecordReceipts(r.ctx, w.Name, ids)
}

// status lists each worker's latest spawn: its goal, whether it landed or ended, and
// whether its recorded process runs. A spawn from before goals reads --goal legacy.
func (r *run) status(fs *flag.FlagSet, args []string) error {
	if err := parse(fs, args); err != nil {
		return err
	}
	sps, err := r.c.Logbook.LatestSpawns(r.ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(r.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKER\tGOAL\tSTATE\tRUNNING\tWORKSPACE")
	var errs []error
	for _, sp := range sps {
		state := "live"
		if sp.Ended {
			state = "ended"
		}
		if _, landed, err := r.c.Logbook.LandedOf(r.ctx, sp.ID); err != nil {
			errs = append(errs, err)
		} else if landed {
			state += ",landed"
		}
		running := "-"
		if !sp.Ended {
			running = r.running(sp)
		}
		g := sp.Goal.String()
		if e, err := r.c.Logbook.Goal(r.ctx, sp); err != nil { // the newest amend, not the spawn's goal
			errs = append(errs, err)
		} else {
			g = e.String()
			if e.Discarded {
				g += " (discarded)"
			}
		}
		fmt.Fprintf(tw, "%s\t--goal %s\t%s\t%s\t%s\n", sp.Name, g, state, running, sp.Workspace.Dir)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return errors.Join(errs...)
}

// running is whether sp's recorded process runs: yes, no, unconfirmed, or why bs
// couldn't look ("couldn't look" is never "no").
func (r *run) running(sp logbook.Spawn) string {
	st, ok, err := r.c.Logbook.LatestStart(r.ctx, sp.ID)
	switch {
	case err != nil:
		return "unknown: " + err.Error()
	case !ok && sp.Legacy():
		return "no start recorded (legacy)"
	case !ok:
		return "unconfirmed"
	}
	alive, err := r.c.Proc.Alive(st.Process)
	switch {
	case err != nil:
		return "unknown: " + err.Error()
	case alive:
		return "yes"
	}
	return "no"
}
