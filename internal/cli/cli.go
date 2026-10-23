// Package cli is bs's command line over the core: cmd/bs runs it on the machine's
// ports, the scenario suite on fakes, with beesfake's relayed calls as the callers.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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

// recorded is the adapter of a harness some entry recorded, any model: it only rings and
// is looked for. nil when this bs has none.
func (p Ports) recorded(name string) port.Harness {
	if p.Harness != nil {
		return p.Harness
	}
	if p.NewHarness != nil {
		return p.NewHarness(name, "")
	}
	return nil
}

// usage says {cmd} for the command a reader runs.
var usage = strings.ReplaceAll(`usage: {cmd} <command> [flags] [arguments]   (flags before arguments)

{cmd} reads ~/.config/bees, or on a herdr --session server that session's config
(~/.config/bees/sessions/<session>).

commands:
  queen                                                    record this pane's queen, then exec the harness
  spawn   --goal <goal>... [--label L] <bead>                start a worker
  done    [--pr URL]... [--replaces URL]... [--answer yes|no --report TEXT|-] [--wait [--timeout D]] [--force]   hand off
  handoff                                                  done --wait
  check   <worker>                                         run its handoff checks now; writes nothing
  goal    <worker> <goal>... | discard | --waive <check>   amend, waive or (keeper) discard
  end     [--force] [--incomplete] <worker>               complete and end a worker
  prune                                                    remove completed workspaces
  send    [--project P] [--now] <to> <message|->           store a message, then ring
  inbox                                                    print unread mail
  status                                                   list workers and their goals
  config                                                   every setting and where it came from
  config  init [--project] [--harness H] [--logbook-dir D] [--repos D] [--worktrees D] [--workspace K] [--repo D]   write a commented config file
  doctor                                                   check what {cmd} needs
  epic    <title>                                          create the project's next epic (<project>-<n>)
  bead    ls [--all] [<bd query>] | new --parent <project>-N [--description D] [--type T] [--priority P] <title>
  version                                                  print the commit {cmd} was built from
  guard   <glob>...                                        agy's PreToolUse hook: deny a tool call on stdin a glob matches

{cmd} acts in the repo it runs in, as its nearest recorded ancestor (a queen, a worker),
else as the keeper. --as <session>, a debug override from a shell no session started,
acts as that session of the project, with its permissions; inbox --as only peeks.
`, "{cmd}", bsname.Cmd)

// Run runs bs args for env on ports, printing to stdout and stderr, and returns the exit
// code.
func Run(ctx context.Context, env Env, ports Ports, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return map[bool]int{true: 2, false: 0}[len(args) == 0]
	}
	if args[0] == "guard" { // a hook: no config, logbook or stale check, and a deny on any error
		return guard(args[1:], env.Stdin, stdout)
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, bsname.Cmd+": unknown command %q\n\n%s", args[0], usage)
		return core.ExitRefused
	}
	args, as, err := cutAs(args)
	if err != nil {
		fmt.Fprintf(stderr, bsname.Cmd+" %s: %v\n", args[0], err)
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
	r := &run{ctx: ctx, name: args[0], env: env, s: s, ports: ports, out: stdout, err: stderr, caller: s.Caller}
	fs := flag.NewFlagSet(bsname.Cmd+" "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	badFlag := false // flag calls Usage on a flag it can't parse: a refusal, never exit 1
	fs.Usage = func() { badFlag = true; fs.PrintDefaults() }
	// done says how a command ended: a bad flag is a refusal, never exit 1.
	done := func(err error) int {
		switch {
		case errors.Is(err, flag.ErrHelp):
			return 0
		case badFlag && err != nil:
			fmt.Fprintf(stderr, "%s %s: %v\n", bsname.Cmd, args[0], err)
			return core.ExitRefused
		}
		return report(stderr, args[0], err)
	}
	rest := args[1:]
	if p := pre[args[0]]; p != nil { // its arguments are checked before any logbook is opened, or made
		if err := p(r, fs, rest); err != nil {
			return done(err)
		}
		rest = nil
	}
	var lb *logbook.Logbook
	if cmd.logbook {
		if err := s.checkLogbook(env); err != nil {
			return report(stderr, args[0], err)
		}
		if err := r.oldSessions(); err != nil {
			return report(stderr, args[0], err)
		}
		lb, err = r.openLogbook()
		if err != nil {
			return report(stderr, args[0], err)
		}
		defer lb.Close()
		if lb.KeptCopy != "" { // the migration's copy aside found one there and kept it
			fmt.Fprintf(stderr, "%s %s: warning: migrated %s, keeping the %s already there, which may be of an older state: this migration's own copy wasn't made\n",
				bsname.Cmd, args[0], s.Logbook, lb.KeptCopy)
		}
		h := ports.Harness
		if h == nil && ports.NewHarness != nil {
			model := s.Worker.ClaudeCode.Model
			if args[0] == "queen" {
				model = s.Queen.ClaudeCode.Model
			}
			h = ports.NewHarness(s.Harness, model)
		}
		other := ports.recorded
		kind, why, _ := s.Kind(s.GitTop)
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
		if err := r.authorize(as); err != nil {
			return report(stderr, args[0], err)
		}
	} else if as != "" {
		return report(stderr, args[0], fmt.Errorf("%w: --as: %s %s acts as nobody", errUsage, bsname.Cmd, args[0]))
	}
	err = cmd.run(r, fs, rest)
	if lb != nil && lb.Created() { // made at its first write (R48: said once)
		fmt.Fprintf(stderr, "%s %s: new logbook for project %s at %s\n", bsname.Cmd, args[0], s.Project, s.Logbook)
	}
	return done(err)
}

// report prints err for command name and returns its exit code: an *Exit's own, 2 for
// usage, else 1.
func report(stderr io.Writer, name string, err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, bsname.Cmd+" %s: %v\n", name, err)
	var x *core.Exit
	switch {
	case errors.As(err, &x):
		return x.Code
	case errors.Is(err, errUsage):
		return core.ExitRefused
	}
	return core.ExitFail
}

var errUsage = errors.New("usage")

// openLogbook opens the project's logbook. Only bs queen and bs spawn, which start a
// project, open a missing one, made at their first write, so a refused one makes none
// (OQ-K1); any other command is refused, since a deleted logbook would come back empty
// without a word (R48, bees-8.12, motif 14). A missing logbook is refused for every
// command when paths.logbook_dir moved away from one that exists: starting afresh would
// split the store.
func (r *run) openLogbook() (*logbook.Logbook, error) {
	starts := r.name == "queen" || r.name == "spawn"
	if starts { // a pin is the likelier move, and the one to name
		if err := r.s.unpinned(r.name); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(r.s.Logbook); errors.Is(err, fs.ErrNotExist) {
		if err := r.moved(); err != nil {
			return nil, err
		}
	}
	if starts {
		return logbook.AtFirstWrite(r.ctx, r.s.Logbook)
	}
	lb, err := logbook.Open(r.ctx, r.s.Logbook)
	if errors.Is(err, logbook.ErrNoLogbook) && r.name == "send" { // a send never makes a project (R48)
		return nil, &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("no logbook for project %s at %s: it is no bees project; nothing stored", r.s.Project, r.s.Logbook),
			Next: bsname.Cmd + " queen, in a herdr pane in its repo, starts it; or send to a project that has one: " + bsname.Cmd + " send --project <project> queen-<project> \"...\""}
	} else if errors.Is(err, logbook.ErrNoLogbook) {
		return nil, &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("no logbook for project %s at %s; nothing was read or written", r.s.Project, r.s.Logbook),
			Next: bsname.Cmd + " queen starts the project; if it had a queen or workers, the logbook was deleted or paths.logbook_dir moved: restore the file rather than start afresh"}
	}
	return lb, err
}

// moved refuses a missing logbook when a value paths.logbook_dir overrides (the default,
// or the global file's under an env pin) holds the project's logbook.
func (r *run) moved() error {
	for _, dir := range r.s.Shadowed["paths.logbook_dir"] {
		old := filepath.Join(r.s.Expand(dir), r.s.Project+".db")
		if _, err := os.Stat(old); err == nil && old != r.s.Logbook {
			return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("no logbook for project %s at %s, but paths.logbook_dir moved there (%s) from %s, which has one; nothing was read or written",
				r.s.Project, r.s.Logbook, r.s.Sources["paths.logbook_dir"], dir),
				Next: "move " + old + " into " + filepath.Dir(r.s.Logbook) + ", or set paths.logbook_dir back"}
		}
	}
	return nil
}

// authorize settles who runs this command (ADR-0010 Amendment 4 Identity): the caller
// load found, its start healed (core.Confirm), --as applied, then the permission table
// (core.May) for the command. A command acting on a worker asks May again with it.
func (r *run) authorize(as string) error {
	c, err := r.c.Confirm(r.ctx, r.caller)
	if err != nil {
		return err
	}
	if r.caller, err = r.c.As(r.ctx, c, as); err != nil {
		return err
	}
	return r.may("")
}

// may asks the permission table whether the caller runs this command on subject.
func (r *run) may(subject string) error {
	cmd := r.name
	if cmd == "handoff" {
		cmd = "done"
	}
	return core.May(r.caller, cmd, r.s.Project, subject)
}

// cutAs takes --as <session> (or --as=<session>) out of args, wherever it is before a
// "--": one global flag (ADR-0010 Amendment 4), not one per command.
func cutAs(args []string) (rest []string, as string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(rest, args[i:]...), as, nil
		case a == "--as" || a == "-as":
			if i+1 >= len(args) {
				return args, "", fmt.Errorf("%w: flag needs an argument: --as", errUsage)
			}
			as, i = args[i+1], i+1
		case strings.HasPrefix(a, "--as=") || strings.HasPrefix(a, "-as="):
			_, as, _ = strings.Cut(a, "=")
		default:
			rest = append(rest, a)
		}
	}
	return rest, as, nil
}

// oldSessions refuses, before the logbook is opened (and so migrated), a command on a
// logbook where a session the old bs started is still open: this bs can neither ring it
// nor see it run (bees-13.9; ADR-0010: end them first). bs end, the way out, and bs
// status only warn.
func (r *run) oldSessions() error {
	what, err := oldSessionsAt(r.ctx, r.s.Logbook)
	if err != nil || what == "" {
		return err
	}
	end := bsname.Cmd + " end <worker>"
	if r.name == "end" || r.name == "status" {
		fmt.Fprintf(r.err, "%s %s: warning: %s\n  next: %s for each\n", bsname.Cmd, r.name, what, end)
		return nil
	}
	return &core.Exit{Code: core.ExitRefused, Msg: what + "; nothing was recorded or migrated",
		Next: end + " for each (it names the herdr workspace to close by hand), then run this again"}
}

// oldSessionsAt names, read-only, the sessions the old bs started that are still open in
// the logbook at path ("" when none, or when the file is missing).
func oldSessionsAt(ctx context.Context, path string) (string, error) {
	lb, err := logbook.OpenReadOnly(ctx, path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil // nothing recorded: openLogbook refuses it, or bs queen or spawn makes it
	} else if err != nil {
		return "", err
	}
	names, err := lb.Unstarted(ctx)
	lb.Close()
	if err != nil || len(names) == 0 {
		return "", err
	}
	n := "an open spawn"
	if len(names) > 1 {
		n = fmt.Sprintf("%d open spawns", len(names))
	}
	return fmt.Sprintf("%s has %s the old bs started, which this bs can neither ring nor see run: %s", path, n, strings.Join(names, ", ")), nil
}

// parse parses args into fs: a bad flag is a usage refusal (exit 2), -h is help.
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil && !errors.Is(err, flag.ErrHelp) {
		return fmt.Errorf("%w: %v", errUsage, err)
	} else if err != nil {
		return err
	}
	return nil
}

type run struct {
	ctx      context.Context
	name     string // the command
	env      Env
	s        *settings
	ports    Ports
	c        *core.Core  // nil for a command that opens no logbook
	caller   core.Caller // who runs it, --as applied and the permission table passed
	out, err io.Writer
	spawnReq spawnArgs // bs spawn's arguments, checked before the logbook is opened
}

// oneGoal is --goal, refused a second time: one goal per spawn (ADR-0010 Amendment 4).
type oneGoal string

func (o *oneGoal) String() string { return string(*o) }
func (o *oneGoal) Set(v string) error {
	if *o != "" {
		return errors.New("--goal given twice: one goal per spawn")
	}
	*o = oneGoal(v)
	return nil
}

// command is one bs command and whether it opens the logbook (refused with no config
// dir, Rule G).
type command struct {
	run     func(r *run, fs *flag.FlagSet, args []string) error
	logbook bool
}

// pre parses and checks a command's arguments before any logbook is opened or made, and
// its run gets none: a spawn refused for its arguments makes no logbook.
var pre = map[string]func(r *run, fs *flag.FlagSet, args []string) error{
	"spawn": (*run).spawnArgs,
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
