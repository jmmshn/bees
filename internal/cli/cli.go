// Package cli is bs's command line over the core: cmd/bs runs it on the machine's
// ports, the scenario suite on fakes, with beesfake's relayed calls as the callers.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/review/github"
	"github.com/jmmshn/bees/internal/vcs/git"
)

// Env is the calling process as bs sees it.
type Env struct {
	Vars  []string // its environment
	Dir   string   // its working directory: the project is the git repo it is in
	Self  int      // its pid: identity and the gate walk from here
	Stdin io.Reader
}

// Ports are the world the core runs on. Harness, when set (tests), is every role's;
// else NewHarness builds the configured one for a role's model, and a harness it doesn't
// know (nil) refuses every command that starts or rings one.
type Ports struct {
	Run        port.Runner
	Harness    port.Harness
	NewHarness func(name, model string) port.Harness
	Proc       port.Proc
	Beads      port.Beads
	// Review and VCS, when set (tests), replace the configured review host and git.
	Review   port.Review
	VCS      port.VCS
	CacheDir string
	Timing   core.Timing
	// Exec replaces bs with argv (argv[0] a path), run in dir with env: bs queen's last
	// step. It returns only on failure.
	Exec func(dir string, env, argv []string) error
	// Revision is the commit bs was built from ("" with no VCS stamp), and Exe the running
	// binary's path: bs version prints the one, and both decide whether bs is stale.
	Revision, Exe string
}

// usage says {cmd} for the command a reader runs.
var usage = strings.ReplaceAll(`usage: {cmd} <command> [flags] [arguments]   (flags before arguments)

{cmd} reads ~/.config/bees, or on a herdr --session server that session's config
(~/.config/bees/sessions/<session>).

commands:
  queen                                                    record this pane's queen, then exec the harness
  spawn   --goal <goal>... [--label L] [--as Q] <bead>   start a worker
  done    [--pr URL]... [--replaces URL]... [--answer yes|no --report TEXT|-] [--wait [--timeout D]] [--force] [--as W]   hand off
  handoff                                                  done --wait
  check   <worker>                                         run its handoff checks now; writes nothing
  goal    [--as Q] <worker> <goal>... | discard | --waive <check>   amend, waive or (keeper) discard
  end     [--force] [--incomplete] [--as Q] <worker>      complete and end a worker
  prune   [--as Q]                                         remove completed workspaces
  send    [--as S] [--project P] [--now] <to> <message|->  store a message, then ring
  inbox   [--as S]                                         print unread mail
  status                                                   list workers and their goals
  config                                                   every setting and where it came from
  config  init [--project] [--harness H] [--logbook-dir D] [--repos D] [--worktrees D] [--workspace K] [--repo D]   write a commented config file
  doctor                                                   check what {cmd} needs
  epic    <title>                                          create the project's next epic (<project>-<n>)
  bead    ls [--all] [<bd query>] | new --parent <project>-N [--description D] [--type T] [--priority P] <title>
  version                                                  print the commit {cmd} was built from
`, "{cmd}", bsname.Cmd)

// Run runs bs args for env on ports, printing to stdout and stderr, and returns the exit
// code.
func Run(ctx context.Context, env Env, ports Ports, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return map[bool]int{true: 2, false: 0}[len(args) == 0]
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, bsname.Cmd+": unknown command %q\n\n%s", args[0], usage)
		return core.ExitRefused
	}
	s, err := load(ctx, env, ports)
	if err != nil && args[0] == "doctor" { // doctor says what broke, and checks the rest
		s, err = broken(env, err)
	}
	warnIfStale(ctx, env, ports, s, stderr)
	if s != nil {
		for _, w := range s.Warnings {
			if s.Missing && len(args) > 1 && args[0] == "config" && args[1] == "init" {
				continue // config init writes the missing dir
			}
			fmt.Fprintf(stderr, bsname.Cmd+" %s: warning: %s\n", args[0], w)
		}
	}
	if err != nil { // a bad file or a refused dir: refused before anything changed
		fmt.Fprintf(stderr, bsname.Cmd+" %s: %v\n", args[0], err)
		var x *core.Exit
		if errors.As(err, &x) {
			return x.Code
		}
		return core.ExitRefused
	}
	if s.Missing && cmd.logbook { // the defaults would name the real logbook (bees-12.26)
		fmt.Fprintf(stderr, bsname.Cmd+" %s: refused: %v %s, so "+bsname.Cmd+" won't guess a logbook; nothing was read or written\n", args[0], errNoConfigDir, s.Session)
		return core.ExitRefused
	}
	r := &run{ctx: ctx, name: args[0], env: env, s: s, ports: ports, out: stdout, err: stderr}
	if cmd.logbook {
		if err := s.checkLogbook(env); err != nil {
			return report(stderr, args[0], err)
		}
		if err := r.oldSessions(); err != nil {
			return report(stderr, args[0], err)
		}
		lb, err := r.openLogbook()
		if err != nil {
			return report(stderr, args[0], err)
		}
		defer lb.Close()
		h := ports.Harness
		if h == nil && ports.NewHarness != nil {
			model := s.Worker.ClaudeCode.Model
			if args[0] == "queen" {
				model = s.Queen.ClaudeCode.Model
			}
			h = ports.NewHarness(s.Harness, model)
		}
		other := func(name string) port.Harness { // a harness some entry recorded, any model: it only rings and is looked for
			if ports.Harness != nil {
				return ports.Harness
			}
			if ports.NewHarness != nil {
				return ports.NewHarness(name, "")
			}
			return nil
		}
		kind, why := s.Kind(s.GitTop)
		review, vcs := ports.Review, ports.VCS
		if review == nil { // review.host's enum has github only (config)
			review = github.Host{Run: ports.Run}
		}
		if vcs == nil {
			vcs = git.VCS{Run: ports.Run}
		}
		r.c = &core.Core{
			Logbook: lb, Project: s.Project, Run: ports.Run, Harness: h, HarnessOf: other, HarnessName: s.Harness,
			Proc: ports.Proc, Beads: ports.Beads, Repo: s.Repo, Worktrees: s.Worktrees, Kind: kind, KindWhy: why,
			ConfigDir: s.Dir, CacheDir: ports.CacheDir, Timing: ports.Timing, WorkerPath: s.workerPath(), Guards: s.Guards, PruneIgnore: s.Prune.Ignore, Review: review, VCS: vcs,
			Warn: func(msg string) { fmt.Fprintf(stderr, bsname.Cmd+" %s: warning: %s\n", args[0], msg) },
		}
	}
	fs := flag.NewFlagSet(bsname.Cmd+" "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	badFlag := false // flag calls Usage on a flag it can't parse: a refusal, never exit 1
	fs.Usage = func() { badFlag = true; fs.PrintDefaults() }
	err = cmd.run(r, fs, args[1:])
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case badFlag && err != nil:
		fmt.Fprintf(stderr, "%s %s: %v\n", bsname.Cmd, args[0], err)
		return core.ExitRefused
	}
	return report(stderr, args[0], err)
}

// report prints err for command name and returns its exit code: an *Exit's own, 2 for
// usage and a refused logbook dir, else 1.
func report(stderr io.Writer, name string, err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, bsname.Cmd+" %s: %v\n", name, err)
	var x *core.Exit
	switch {
	case errors.As(err, &x):
		return x.Code
	case errors.Is(err, errUsage), errors.Is(err, ErrRealLogbook):
		return core.ExitRefused
	}
	return core.ExitFail
}

var errUsage = errors.New("usage")

// openLogbook opens the project's logbook. Only bs queen and bs spawn, which start a
// project, make a missing one, and say so; any other command is refused, since a deleted
// logbook would come back empty without a word (R48, bees-8.12, motif 14).
func (r *run) openLogbook() (*logbook.Logbook, error) {
	if r.name != "queen" && r.name != "spawn" {
		lb, err := logbook.Open(r.ctx, r.s.Logbook)
		if errors.Is(err, logbook.ErrNoLogbook) {
			return nil, &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("no logbook for project %s at %s; nothing was read or written", r.s.Project, r.s.Logbook),
				Next: bsname.Cmd + " queen starts the project; if it had a queen or workers, the logbook was deleted or paths.logbook_dir moved: restore the file rather than start afresh"}
		}
		return lb, err
	}
	lb, created, err := logbook.Create(r.ctx, r.s.Logbook)
	if created {
		fmt.Fprintf(r.err, "%s %s: new empty logbook for project %s at %s\n", bsname.Cmd, r.name, r.s.Project, r.s.Logbook)
	}
	return lb, err
}

// oldSessions refuses, before the logbook is opened (and so migrated), a command on a
// logbook where a session the old bs started is still open: this bs can neither ring it
// nor see it run (bees-13.9; ADR-0010: end them first). bs end, the way out, and bs
// status only warn.
func (r *run) oldSessions() error {
	lb, err := logbook.OpenReadOnly(r.ctx, r.s.Logbook)
	if errors.Is(err, os.ErrNotExist) {
		return nil // nothing recorded: openLogbook refuses it, or bs queen or spawn makes it
	} else if err != nil {
		return err
	}
	names, err := lb.Unstarted(r.ctx)
	lb.Close()
	if err != nil || len(names) == 0 {
		return err
	}
	n := "an open spawn"
	if len(names) > 1 {
		n = fmt.Sprintf("%d open spawns", len(names))
	}
	what := fmt.Sprintf("%s has %s the old bs started, which this bs can neither ring nor see run: %s", r.s.Logbook, n, strings.Join(names, ", "))
	if r.name == "end" || r.name == "status" {
		fmt.Fprintf(r.err, "%s %s: warning: %s\n  next: %s end <worker> for each\n", bsname.Cmd, r.name, what, bsname.Cmd)
		return nil
	}
	return &core.Exit{Code: core.ExitRefused, Msg: what + "; nothing was read or written",
		Next: fmt.Sprintf("%s end <worker> for each (it names the herdr workspace to close by hand), then run this again", bsname.Cmd)}
}

type run struct {
	ctx      context.Context
	name     string // the command
	env      Env
	s        *settings
	ports    Ports
	c        *core.Core // nil for a command that opens no logbook
	out, err io.Writer
}

// list is a repeatable flag.
type list []string

func (l *list) String() string     { return strings.Join(*l, " ") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

// who is the caller: its recorded pane inside one, else --as (ADR-0010 identity), in an
// unrecorded herdr pane too (bees-13.14). load has already ignored, with a warning, every
// BS_* variable inside a recorded pane.
func (r *run) who(as string) (core.Who, error) {
	if p := r.s.Pane; p != nil {
		w, err := r.c.Identify(r.ctx, *p, r.env.Self)
		if err != nil {
			return core.Who{}, err
		}
		if as != "" && as != w.Name {
			return core.Who{}, fmt.Errorf("%w: --as %s inside %s's recorded pane", errUsage, as, w.Name)
		}
		return w, nil
	}
	if as == "" {
		return core.Who{}, fmt.Errorf("%w: %v: name the session with --as", errUsage, core.ErrOutsidePane)
	}
	sp, ok, err := r.c.Logbook.LatestSpawn(r.ctx, as)
	if err != nil {
		return core.Who{}, err
	}
	if ok && !sp.Ended {
		return core.Who{Name: as, Spawn: sp.ID}, nil
	}
	return core.Who{Name: as, Queen: strings.HasPrefix(as, "queen-")}, nil
}

// command is one bs command and whether it opens the logbook (refused with no config
// dir, Rule G).
type command struct {
	run     func(r *run, fs *flag.FlagSet, args []string) error
	logbook bool
}

var commands = map[string]command{
	"queen":   {(*run).queen, true},
	"spawn":   {(*run).spawn, true},
	"done":    {(*run).done, true},
	"handoff": {(*run).done, true}, // the glossary's word for it: done, waiting by default
	"check":   {(*run).check, true},
	"goal":    {(*run).goal, true},
	"end":     {(*run).end, true},
	"prune":   {(*run).prune, true},
	"send":    {(*run).send, true},
	"inbox":   {(*run).inbox, true},
	"status":  {(*run).status, true},
	"config":  {(*run).config, false},
	"doctor":  {(*run).doctor, false},
	"epic":    {(*run).epic, false},
	"bead":    {(*run).bead, false},
	"version": {(*run).version, false},
}
