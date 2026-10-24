package cli

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/checks"
	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/harness/agy"
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
	if w := r.s.Caller; w.Recorded() {
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("%s runs this bs already", w.Name), Next: "start the queen in a pane of her own"}
	}
	if r.c.Harness == nil {
		return &core.Exit{Code: core.ExitRefused, Msg: "no harness adapter for harness = " + r.s.Harness, Next: bsname.Cmd + " config"}
	}
	program := r.s.program(r.s.Harness)
	exe, ok := checks.LookPath(core.Getenv(r.env.Vars, "PATH"), program)
	if !ok {
		msg := program + " is not on PATH"
		if strings.Contains(program, "/") {
			msg = program + " is missing or not executable (agy.bin)"
		}
		return &core.Exit{Code: core.ExitRefused, Msg: msg, Next: bsname.Cmd + " doctor"}
	}
	if err := r.c.CheckGuards(r.ctx); err != nil { // bees-12.20: refused now, not at every spawn
		return err
	}
	argv, unset, err := r.c.Queen(r.ctx, core.QueenReq{Session: r.s.Session, Pane: pane, Self: r.env.Self, Exe: exe})
	if err != nil {
		return err
	}
	// bs sets no BS_* in a session's env (ADR-0010): her bs finds her by her pane. What the
	// harness must not inherit goes too (agy's SSH_*, bees-13.13.13).
	env := slices.DeleteFunc(slices.Clone(r.env.Vars), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return strings.HasPrefix(k, "BS_") || slices.Contains(unset, k)
	})
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
	goal    oneGoal
	label   string
	harness string
	bead    string
}

// spawnArgs parses bs spawn's arguments and, when the project has no logbook yet, checks
// its bead exists, before the logbook is opened or made (bees-13.9 review S3).
func (r *run) spawnArgs(fs *flag.FlagSet, args []string) error {
	a := &r.spawnReq
	fs.Var(&a.goal, "goal", "what the worker hands off (one)")
	fs.StringVar(&a.label, "label", "", "its herdr label: 1-4 lowercase words (default: its last spawn's)")
	fs.StringVar(&a.harness, "harness", "", "the harness this one spawn starts on, recorded on it (default: harness)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: "+bsname.Cmd+" spawn --goal <goal> <bead>", errUsage)
	}
	a.bead = fs.Arg(0)
	// A custom type Spawn resolves against the config; a built-in one is checked here too,
	// before a logbook is made.
	if _, custom := r.s.Goal[goal.TypeOf(string(a.goal))]; !custom {
		if _, err := goal.Parse(string(a.goal)); err != nil {
			return &core.Exit{Code: core.ExitRefused, Msg: a.bead, Err: err}
		}
	}
	if a.label != "" {
		if err := core.CheckLabel(a.label); err != nil { // likewise
			return &core.Exit{Code: core.ExitRefused, Msg: a.bead + ": --label", Err: err}
		}
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
	if r.c.Harness == nil && a.harness == "" { // --harness names its own: Spawn checks that one
		return &core.Exit{Code: core.ExitRefused, Msg: "no harness adapter for harness = " + r.s.Harness, Next: bsname.Cmd + " config"}
	}
	// A kind the config chose and spawn can't make yet is refused, never made a worktree;
	// a worked-out none is left to Spawn, which names why the repo isn't a checkout.
	if r.s.RepoMissing {
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("the repo %s (%s) doesn't exist", r.s.Repo, r.s.RepoFrom), Next: "set paths.repo to the project's checkout"}
	}
	if kind, why, chosen := r.s.Kind(r.s.GitTop); chosen && unmade(kind) != "" {
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("workspace kind %s (%s): %s", kind, why, unmade(kind))}
	} else if err := r.s.Workspace.Command.Incomplete(); kind == config.CommandKind && err != nil {
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("workspace kind command (%s); nothing was made", why), Err: err, Next: bsname.Cmd + " config"}
	}
	// The keeper's spawn too is the queen's: its worker hands off to her.
	sp, resumed, err := r.c.Spawn(r.ctx, core.SpawnReq{By: core.Queen(r.s.Project), Session: cmp.Or(core.Getenv(r.env.Vars, "HERDR_SESSION"), logbook.DefaultSession),
		Bead: a.bead, Label: a.label, Goal: string(a.goal), Harness: a.harness})
	if err != nil {
		return err
	}
	if resumed {
		fmt.Fprintf(r.out, "resumed %s in pane %s on herdr session %s, in %s\n", sp.Name, sp.Herdr.Pane, sp.Herdr.Session, sp.Workspace.Dir)
	} else {
		fmt.Fprintf(r.out, "spawned %s (--goal %s) in %s, pane %s on herdr session %s\n", sp.Name, sp.Goal, sp.Workspace.Dir, sp.Herdr.Pane, sp.Herdr.Session)
	}
	if sp.Harness == agy.Name {
		r.agyProbed()
		r.agyTook(sp.Name)
	}
	return nil
}

// agyProbed warns when the configured agy binary is not agy.Probed, the release bs's agy
// contracts were last probed on (bees-13.13.15). An absent binary says nothing here:
// tests run on beesfake with no agy on PATH, and bs doctor checks the binary.
func (r *run) agyProbed() {
	d := &doctor{r: r, path: core.Getenv(r.env.Vars, "PATH")}
	prog := r.s.program(agy.Name)
	if _, ok := checks.LookPath(d.path, prog); !ok {
		return
	}
	switch at, v, err := d.version(prog); {
	case err != nil && v == "":
		fmt.Fprintf(r.err, "%s spawn: warning: can't read %s (%s's agy contracts were probed on %s)\n  next: %s doctor\n", bsname.Cmd, err, bsname.Cmd, agy.Probed, bsname.Cmd)
	case v != agy.Probed:
		fmt.Fprintf(r.err, "%s spawn: warning: %s is %s, not %s (the version %s's agy contracts were probed on): re-probe the hook payloads, the --mode log line, and docs/adr/0011-ring-by-push-where-the-harness-allows.md\n  next: %s doctor\n", bsname.Cmd, at, v, agy.Probed, bsname.Cmd, bsname.Cmd)
	}
}

// agyTook warns when the agy just started for session name says in its log it didn't
// take the mode bs asked for (bees-13.13.14). agy logs it at once, before herdr's start
// returns; a log not found yet says nothing, and bs doctor checks again.
func (r *run) agyTook(name string) {
	if t, ok, err := r.s.agyLogs().Took(name); err == nil && ok && t.Fallback != "" {
		fmt.Fprintf(r.err, "%s spawn: warning: %s's agy didn't take --mode %s, so it asks about every edit and command; its log %s says: %s\n  next: %s doctor\n", bsname.Cmd, name, cmp.Or(t.Asked, "?"), t.Log, t.Fallback, bsname.Cmd)
	}
}

func (r *run) done(fs *flag.FlagSet, args []string) error {
	answer := fs.String("answer", "", "a verdict goal's answer (yes or no) or a review goal's (approve or changes)")
	report := fs.String("report", "", "a verdict or review goal's report, or - to read it from stdin")
	force := fs.Bool("force", false, "hand off while background work runs (waives that gate only)")
	wait := fs.Bool("wait", r.name == "handoff", "while CI is still running (or the host hasn't shown a push yet), wait for it (up to --timeout), then hand off or name what failed")
	timeout := fs.Duration("timeout", 8*time.Minute, "how long --wait waits, at most 9m (bs gives a worker's shell calls 10)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: "+bsname.Cmd+" done takes no arguments; say it with --report (got %q)", errUsage, strings.Join(fs.Args(), " "))
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
	d, err := r.c.Done(r.ctx, r.caller, r.env.Self, core.Handed{Answer: *answer, Report: *report}, core.DoneOpts{Force: *force, Wait: waitFor(*wait, *timeout)})
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
	if err := r.may(name); err != nil {
		return err
	}
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("%w: "+bsname.Cmd+" goal <worker> <goal> | discard | --waive <check>", errUsage)
	}
	name := core.Name(fs.Arg(0))
	// ADR-0010 spells it bs goal <name> --waive <check>: flags after the worker too.
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return err
	}
	if slices.Equal(fs.Args(), []string{"discard"}) {
		if err := core.May(r.caller, "goal:discard", r.s.Project, name); err != nil {
			return err
		}
	}
	e, err := r.c.Goal(r.ctx, core.GoalReq{By: r.caller.Name, Name: name, Values: fs.Args(), Waive: *waive})
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
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: "+bsname.Cmd+" end <worker>", errUsage)
	}
	name := core.Name(fs.Arg(0))
	if err := r.c.End(r.ctx, r.caller.Name, name, r.env.Self, core.EndOpts{Force: *force, Incomplete: *incomplete}); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "ended %s; its workspace stays for "+bsname.Cmd+" prune\n", name)
	return nil
}

func (r *run) prune(fs *flag.FlagSet, args []string) error {
	if err := parse(fs, args); err != nil {
		return err
	}
	ps, err := r.c.Prune(r.ctx, r.caller.Name)
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
	project := fs.String("project", "", "write into another project's logbook (its session reads it there)")
	now := fs.Bool("now", false, "ring with priority now: interrupt the recipient's current turn")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("%w: "+bsname.Cmd+" send [--project P] [--now] <to> <message | ->", errUsage)
	}
	w := r.caller
	to, body := core.Name(fs.Arg(0)), fs.Arg(1) // a bead id names its worker (ADR-0006)
	if err := r.may(to); err != nil {
		return err
	}
	if body == "-" {
		b, err := io.ReadAll(r.env.Stdin)
		if err != nil {
			return err
		}
		body = strings.TrimRight(string(b), "\n")
	}
	if strings.TrimSpace(body) == "" {
		return &core.Exit{Code: core.ExitRefused, Msg: "empty message; nothing stored", Next: bsname.Cmd + " send " + to + ` "<message>"`}
	}
	// A send crossing projects records the sender's project, so the reply needs no project
	// read back out of a name (T11, motif 3): a session's own, else the keeper's repo's.
	c, from := r.c, r.s.Project
	if w.Recorded() {
		from = w.Project
	}
	if p := config.Normalise(*project); p != "" && p != r.s.Project {
		// --project writes into p's logbook; only that logbook changes: the sender stays
		// who it is.
		path := filepath.Join(filepath.Dir(r.s.Logbook), p+".db")
		// The other logbook gets the same old-session refusal, before Open migrates it.
		if what, err := oldSessionsAt(r.ctx, path); err != nil {
			return &core.Exit{Code: core.ExitCantCheck, Msg: fmt.Sprintf(bsname.Cmd+" send --project %s: can't read %s; nothing stored", p, path), Err: err, Next: bsname.Cmd + " doctor"}
		} else if what != "" {
			return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("%s send --project %s: %s; nothing stored", bsname.Cmd, p, what),
				Next: "its queen or keeper ends them first: " + bsname.Cmd + " end <worker>, in " + p + "'s repo"}
		}
		lb, err := logbook.Open(r.ctx, path)
		if errors.Is(err, logbook.ErrNoLogbook) {
			return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf(bsname.Cmd+" send --project %s: %s is no bees project (no logbook at %s); nothing stored", p, p, path),
				Next: "check the project name against the logbooks: ls " + filepath.Dir(path)}
		} else if err != nil {
			return &core.Exit{Code: core.ExitCantCheck, Msg: fmt.Sprintf(bsname.Cmd+" send --project %s: can't read %s; nothing stored", p, path), Err: err, Next: bsname.Cmd + " doctor"}
		}
		defer lb.Close()
		cc := *r.c
		cc.Logbook, cc.Project, cc.Repo = lb, p, "" // p's repo: her last queen entry's
		c = &cc
	}
	if from == c.Project {
		from = ""
	}
	priority := port.Next
	if *now {
		priority = port.Now
	}
	sent, err := c.Send(r.ctx, core.SendIn{From: w, FromProject: from, To: to, Body: body, Priority: priority,
		LogbookDir: filepath.Dir(r.s.Logbook), Bs: r.sharedBs(), Session: r.s.Session})
	if err != nil {
		return err
	}
	fmt.Fprintf(r.out, "sent %s to %s\n", sent.ID, to)
	if sent.Stranger {
		fmt.Fprintf(r.err, bsname.Cmd+" send: warning: %s is neither %s nor a worker spawned in project %s: stored, not rung; it reads it only as a session of %s, and a worker spawned as %s later reads only mail sent after its spawn\n",
			to, core.Queen(c.Project), c.Project, c.Project, to)
		return nil
	}
	switch q := sent.Started; {
	case q.Pane != "":
		fmt.Fprintf(r.out, "started %s in pane %s on herdr session %s (%s queen)\n", to, q.Pane, q.Session, sent.Bs)
		if sent.Rung != nil && !errors.Is(sent.Rung, core.ErrEnded) && !errors.Is(sent.Rung, port.ErrArmed) {
			fmt.Fprintf(r.out, "stored, not rung: %s doesn't take a ring yet (%v); her pane may be at a prompt, such as folder trust: herdr --session %s pane read %s --source recent\n", to, sent.Rung, q.Session, q.Pane)
			return nil
		}
	case errors.Is(sent.NotStarted, core.ErrStarting):
		fmt.Fprintf(r.out, "stored; %s, by another send\n", sent.NotStarted)
	case sent.NotStarted != nil:
		fmt.Fprintf(r.err, bsname.Cmd+" send: warning: %s isn't running and wasn't started: %v\n  next: "+bsname.Cmd+" queen, in a herdr pane in %s's repo\n", to, sent.NotStarted, c.Project)
	}
	r.rang(to, sent.Rung)
	return nil
}

// sharedBs is the bs on the caller's PATH a crossing send starts another project's queen
// with (OQ-K7): never a worker's build, so worker.path's dirs and the caller's own
// workspace are skipped. "" when there is none.
func (r *run) sharedBs() string {
	skip := append(r.s.workerPath(), r.caller.Dir())
	var dirs []string
	for _, d := range filepath.SplitList(core.Getenv(r.env.Vars, "PATH")) {
		if !slices.ContainsFunc(skip, func(s string) bool { return s != "" && within(d, s) }) {
			dirs = append(dirs, d)
		}
	}
	at, _ := checks.LookPath(strings.Join(dirs, string(filepath.ListSeparator)), bsname.Cmd)
	return at
}

// within reports whether dir is root or under it.
func within(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ring rings to and says how it went.
func (r *run) ring(c *core.Core, from, to string, p port.Priority) {
	r.rang(to, c.Ring(r.ctx, from, to, p))
}

// rang says how ringing to went. The message is already stored, so no outcome fails the
// command: the logbook entry is the delivery, the ring only when it is read (motif 10).
func (r *run) rang(to string, err error) {
	switch {
	case err == nil:
		fmt.Fprintf(r.out, "rung %s\n", to)
	case errors.Is(err, port.ErrArmed):
		fmt.Fprintf(r.out, "stored; %s's %v to read it\n", to, err)
	case errors.Is(err, port.ErrNotRunning), errors.Is(err, port.ErrNoRing):
		fmt.Fprintf(r.out, "stored, not rung (%v): %s reads it at its next "+bsname.Cmd+" inbox\n", err, to)
	case errors.Is(err, core.ErrEnded):
		fmt.Fprintf(r.out, "stored, not rung: %v, so nobody reads it (a later spawn reads only mail sent after it)\n", err)
	default:
		fmt.Fprintf(r.err, bsname.Cmd+" %s: couldn't ring %s (the message is stored): %v\n", r.name, to, err)
	}
}

func (r *run) inbox(fs *flag.FlagSet, args []string) error {
	all := fs.Bool("all", false, "include read mail; marks nothing read")
	wait := fs.Bool("wait", false, "wait for new mail, say who it's from and exit, marking nothing read: run it in the background, and its exit wakes an idle agy session")
	timeout := fs.Duration("timeout", time.Hour, "how long --wait waits")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *all && *wait {
		return fmt.Errorf("%w: "+bsname.Cmd+" inbox [--all | --wait [--timeout D]]", errUsage)
	}
	w := r.caller
	if *wait {
		ms, err := r.c.Watch(r.ctx, w, r.env.Self, *timeout)
		if err != nil {
			return err
		}
		fmt.Fprintln(r.out, core.Doorbell(ms, ms[len(ms)-1].From, r.c.Project))
		return nil
	}
	var ms []logbook.Message
	var err error
	if *all {
		ms, err = r.c.Logbook.Inbox(r.ctx, w.Name)
	} else {
		ms, err = r.c.Logbook.Unread(r.ctx, w.Name)
	}
	if err != nil {
		return err
	}
	if len(ms) == 0 {
		if *all {
			fmt.Fprintln(r.out, "no messages")
		} else {
			fmt.Fprintln(r.out, "no unread messages")
		}
		return nil
	}
	var ids []logbook.ID
	for _, m := range ms {
		fmt.Fprintf(r.out, "── %s from %s · %s\n%s\n\n", m.ID, m.From, m.TS.Local().Format("2006-01-02 15:04:05"), m.Body)
		if m.FromProject != "" { // cross-project mail: the reply goes queen to queen, into the sender's project
			fmt.Fprintf(r.out, "   (reply with: "+bsname.Cmd+" send --project %s %s \"...\")\n\n", m.FromProject, core.Queen(m.FromProject))
		}
		ids = append(ids, m.ID)
	}
	if *all {
		return nil
	}
	if w.Peek { // a debug peek: only the session itself marks its mail read
		fmt.Fprintf(r.out, "(peeked as %s with --as: nothing marked read)\n", w.Name)
		return nil
	}
	return r.c.Logbook.RecordReceipts(r.ctx, w.Name, ids)
}

// status lists each worker's latest spawn: its label (its herdr workspace's), its goal,
// whether it ended, its handoff, whether its goal is complete (landed, and by what),
// whether its recorded process runs, and its unread mail; then a row for each other
// session with unread mail (a queen with no recorded start, a stranger). Then one line per worker the table alone would leave a question about, each with
// the next command (Rule G): stuck (its process is gone, its start unconfirmed, or it
// ended incomplete), kept (complete or discarded, its workspace still on disk), waived,
// discarded, late (a start healed after confirmation missed it), and earlier (a
// completion an incomplete respawn's row would hide). It reads the
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
	fmt.Fprintln(r.out, r.as())
	tw := tabwriter.NewWriter(r.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKER\tLABEL\tGOAL\tSTATE\tHANDOFF\tCOMPLETE\tRUNNING\tUNREAD\tWORKSPACE")
	var errs []error
	counts, cerr := r.c.Logbook.UnreadCounts(r.ctx)
	if cerr != nil {
		errs = append(errs, cerr)
	}
	shown := map[string]bool{}
	unread := func(name string) string { // couldn't read it is never 0 (Rule G)
		shown[name] = true
		if cerr != nil {
			return "unknown"
		}
		return strconv.Itoa(counts[name])
	}
	var notes []string
	note := func(kind, name, format string, a ...any) {
		notes = append(notes, kind+": "+name+": "+fmt.Sprintf(format, a...))
	}
	queen := core.Queen(r.s.Project)
	if q, ok, err := r.c.Logbook.LatestQueen(r.ctx, queen); err != nil {
		errs = append(errs, err)
	} else if ok {
		fmt.Fprintf(tw, "%s\t-\t-\tqueen\t-\t-\t%s\t%s\tpane %s on herdr session %s\n", queen, alive(r.c.Proc.Alive(q.Process)), unread(queen), q.Pane, q.Session)
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
		running, mail := "-", "-" // an ended spawn reads no mail
		if !sp.Ended {
			running, mail = r.running(sp), unread(sp.Name)
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
		fmt.Fprintf(tw, "%s\t%s\t--goal %s\t%s\t%s\t%s\t%s\t%s\t%s\n", sp.Name, cmp.Or(sp.Label, "-"), g, state, handoff, complete, running, mail, dir)
		switch {
		case running == "no start recorded (legacy)":
			note("stuck", sp.Name, "started by the old bs, which recorded no process → %s end %s", bsname.Cmd, sp.Name)
		case running == "unconfirmed":
			note("stuck", sp.Name, "its start was never confirmed → %s end --incomplete %s", bsname.Cmd, sp.Name)
		case strings.HasPrefix(running, "no"):
			next := bsname.Cmd + " end " + sp.Name
			if !landed {
				next = bsname.Cmd + " end --incomplete " + sp.Name + " if its goal isn't complete"
			}
			if discarded || gerr != nil { // nothing to resume for: only the end
				note("stuck", sp.Name, "its process is gone but it isn't ended → %s", next)
			} else {
				note("stuck", sp.Name, "its process is gone but it isn't ended → %s spawn --goal %s %s resumes it in its pane, or %s", bsname.Cmd, e.Goal, sp.Bead, next)
			}
		case lerr != nil || gerr != nil || derr != nil: // couldn't read it: never read as "no" (Rule G)
		case sp.Ended && !landed && !sp.Legacy() && !discarded && !handed:
			// Completion needs a passing handoff, and an ended worker hands nothing off.
			note("stuck", sp.Name, "ended with nothing handed off, so its goal can't complete → the keeper discards it (%s goal %s discard, bees-13.6)", bsname.Cmd, sp.Name)
		case sp.Ended && !landed && !sp.Legacy() && !discarded:
			note("stuck", sp.Name, "ended with its goal incomplete → %s prune completes it once --goal %s holds; if its change closed unmerged it never will, so the keeper discards it (%s goal %s discard)", bsname.Cmd, g, bsname.Cmd, sp.Name)
		case sp.Ended && (landed || discarded) && (sp.Workspace.Kind == config.GitWorktree || sp.Workspace.Kind == config.CommandKind) && sp.Workspace.Dir != "":
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
		// An earlier spawn's completion stays visible under the respawn's row (bees-13.78.4).
		if lerr == nil && !landed {
			if prev, ok, err := r.c.Logbook.EarlierLanded(r.ctx, sp.Name, sp.ID); err != nil {
				errs = append(errs, err)
			} else if ok {
				note("earlier", sp.Name, "its spawn of %s (--goal %s) is complete; this row is a later spawn's", prev.TS.Local().Format("01-02 15:04"), prev.Goal)
			}
		}
		if st, ok, err := r.c.Logbook.LatestStart(r.ctx, sp.ID); err != nil {
			errs = append(errs, err)
		} else if ok && st.Late && !sp.Ended {
			note("late", sp.Name, "its start was recorded late, after confirmation missed it (pid %d) → nothing to do; %s end %s if it misbehaves", st.PID, bsname.Cmd, sp.Name)
		}
	}
	// Mail waits for sessions no row above names: a queen never recorded starting, a
	// stranger. Mail the table hides is mail nobody reads.
	for _, name := range slices.Sorted(maps.Keys(counts)) {
		if counts[name] > 0 && !shown[name] {
			fmt.Fprintf(tw, "%s\t-\t-\tno spawn\t-\t-\t-\t%d\t-\n", name, counts[name])
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

// as is status's first line: who bs takes the caller for, and where it read that.
func (r *run) as() string {
	w := r.caller
	what := fmt.Sprintf("%s of %s", w.Role, cmp.Or(w.Project, r.s.Project))
	if w.Role == core.Foreign {
		what = fmt.Sprintf("worker of %s, acting in %s", w.Project, r.s.Project)
		if strings.HasPrefix(w.Name, "queen-") {
			what = fmt.Sprintf("queen of %s, acting in %s", w.Project, r.s.Project)
		}
	}
	if !w.Spawned.IsZero() {
		what += ", spawned " + w.Spawned.Local().Format("01-02 15:04")
	}
	switch w.How {
	case core.ByTripwire:
		what += ", by the tripwire: its pane is in the worker's workspace"
	case core.ByAs:
		what += ", by --as"
	}
	return fmt.Sprintf("as %s (%s) · logbook %s", w.Name, what, r.s.Logbook)
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
