// Package cli is bsx's command line over the core: cmd/bsx runs it on the machine's
// ports, the scenario suite on fakes, with beesfake's relayed calls as the callers.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/jmmshn/bees/cmd/bsx/internal/core"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// Env is the calling process as bs sees it.
type Env struct {
	Vars  []string // its environment
	Dir   string   // its working directory: the project is the git repo it is in
	Self  int      // its pid: identity and the gate walk from here
	Stdin io.Reader
}

// Ports are the world the core runs on. A nil Harness refuses every command that starts
// one: the claude_code adapter arrives with bees-13.3.
type Ports struct {
	Run      port.Runner
	Harness  port.Harness
	Proc     port.Proc
	Beads    port.Beads
	CacheDir string
	Timing   core.Timing
}

const usage = `usage: bsx <command> [flags] [arguments]   (flags before arguments)

bsx is the ADR-0010 rebuild of bs; until the swap (bees-13.9) it runs only on a
herdr --session server, on that session's config (~/.config/bees/sessions/<session>).

commands:
  spawn   --goal <goal>... [--label L] [--as Q] <bead>   start a worker
  done    [--pr URL]... [--answer yes|no --report TEXT|-] [--force] [--as W]   hand off
  end     [--force] [--incomplete] [--as Q] <worker>      complete and end a worker
  prune   [--as Q]                                         remove completed workspaces
  send    [--as S] <to> <message>                          store a message, then ring
  inbox   [--as S]                                         print unread mail
  status                                                   list workers and their goals
  config                                                   every setting and where it came from
  doctor                                                   check what bsx needs
`

// Run runs bsx args for env on ports, printing to stdout and stderr, and returns the exit
// code.
func Run(ctx context.Context, env Env, ports Ports, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return map[bool]int{true: 2, false: 0}[len(args) == 0]
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "bsx: unknown command %q\n\n%s", args[0], usage)
		return core.ExitRefused
	}
	s, err := load(ctx, env, ports.Run)
	if err != nil { // a bad file or a refused dir: refused before anything changed
		fmt.Fprintf(stderr, "bsx %s: %v\n", args[0], err)
		var x *core.Exit
		if errors.As(err, &x) {
			return x.Code
		}
		return core.ExitRefused
	}
	for _, w := range s.Warnings {
		fmt.Fprintf(stderr, "bsx %s: warning: %s\n", args[0], w)
	}
	if s.Missing && cmd.writes {
		fmt.Fprintf(stderr, "bsx %s: refused: %v %s; nothing was written\n", args[0], errNoConfigDir, s.Session)
		return core.ExitRefused
	}
	r := &run{ctx: ctx, env: env, s: s, ports: ports, out: stdout, err: stderr}
	if cmd.logbook {
		if err := s.checkLogbook(env); err != nil {
			return report(stderr, args[0], err)
		}
		lb, err := logbook.Open(ctx, s.Logbook)
		if err != nil {
			return report(stderr, args[0], err)
		}
		defer lb.Close()
		r.c = &core.Core{
			Logbook: lb, Project: s.Project, Run: ports.Run, Harness: ports.Harness, HarnessName: s.Harness,
			Proc: ports.Proc, Beads: ports.Beads, Repo: s.Repo, Worktrees: s.Worktrees,
			ConfigDir: s.Dir, CacheDir: ports.CacheDir, Timing: ports.Timing,
		}
	}
	fs := flag.NewFlagSet("bsx "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := cmd.run(r, fs, args[1:]); !errors.Is(err, flag.ErrHelp) {
		return report(stderr, args[0], err)
	}
	return 0
}

// report prints err for command name and returns its exit code: an *Exit's own, 2 for
// usage and a refused logbook dir, else 1.
func report(stderr io.Writer, name string, err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "bsx %s: %v\n", name, err)
	var x *core.Exit
	switch {
	case errors.As(err, &x):
		return x.Code
	case errors.Is(err, errUsage), errors.Is(err, ErrDefaultConfig):
		return core.ExitRefused
	}
	return core.ExitFail
}

var errUsage = errors.New("usage")

type run struct {
	ctx      context.Context
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

// who is the caller: its recorded pane inside one, else --as (ADR-0010 identity). load
// has already ignored, with a warning, every BS_* variable inside a pane.
func (r *run) who(as string) (core.Who, error) {
	w, err := r.c.Identify(r.ctx, r.env.Vars, r.env.Self)
	if err == nil {
		if as != "" && as != w.Name {
			return core.Who{}, fmt.Errorf("%w: --as %s inside %s's recorded pane", errUsage, as, w.Name)
		}
		return w, nil
	}
	if !errors.Is(err, core.ErrOutsidePane) {
		return core.Who{}, err
	}
	if as == "" {
		return core.Who{}, fmt.Errorf("%w: not in a herdr pane: name the session with --as", errUsage)
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

// command is one bsx command: whether it writes (refused with no config dir, Rule G) and
// whether it opens the logbook.
type command struct {
	run             func(r *run, fs *flag.FlagSet, args []string) error
	writes, logbook bool
}

var commands = map[string]command{
	"spawn":  {(*run).spawn, true, true},
	"done":   {(*run).done, true, true},
	"end":    {(*run).end, true, true},
	"prune":  {(*run).prune, true, true},
	"send":   {(*run).send, true, true},
	"inbox":  {(*run).inbox, true, true}, // it writes receipts
	"status": {(*run).status, false, true},
	"config": {(*run).config, false, false},
	"doctor": {(*run).doctor, false, false},
}
