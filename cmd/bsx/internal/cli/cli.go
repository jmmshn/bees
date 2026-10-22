// Package cli is bsx's command line over the core: cmd/bsx runs it on the machine's
// ports, the scenario suite on fakes, with beesfake's relayed calls as the callers.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/jmmshn/bees/cmd/bsx/internal/core"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// Env is the calling process as bs sees it.
type Env struct {
	Vars  []string // its environment
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
	cfg, err := loadConfig(env.Vars)
	if err != nil {
		fmt.Fprintf(stderr, "bsx %s: %v\n", args[0], err)
		return core.ExitRefused
	}
	lb, err := logbook.Open(ctx, filepath.Join(cfg.Paths.LogbookDir, cfg.Project+".db"))
	if err != nil {
		fmt.Fprintf(stderr, "bsx %s: %v\n", args[0], err)
		return core.ExitFail
	}
	defer lb.Close()
	c := &core.Core{
		Logbook: lb, Project: cfg.Project, Run: ports.Run, Harness: ports.Harness, HarnessName: cfg.Harness,
		Proc: ports.Proc, Beads: ports.Beads, Repo: cfg.Paths.Repo, Worktrees: cfg.Paths.Worktrees,
		ConfigDir: cfg.Dir, CacheDir: ports.CacheDir, Timing: ports.Timing,
	}
	r := &run{ctx: ctx, env: env, c: c, out: stdout, err: stderr}
	fs := flag.NewFlagSet("bsx "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	err = cmd(r, fs, args[1:])
	var x *core.Exit
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, &x):
		fmt.Fprintf(stderr, "bsx %s: %v\n", args[0], x)
		return x.Code
	case errors.Is(err, errUsage):
		fmt.Fprintf(stderr, "bsx %s: %v\n", args[0], err)
		return core.ExitRefused
	}
	fmt.Fprintf(stderr, "bsx %s: %v\n", args[0], err)
	return core.ExitFail
}

var errUsage = errors.New("usage")

type run struct {
	ctx      context.Context
	env      Env
	c        *core.Core
	out, err io.Writer
}

// list is a repeatable flag.
type list []string

func (l *list) String() string     { return strings.Join(*l, " ") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

// who is the caller: its recorded pane inside one, else --as (ADR-0010 identity). BS_*
// variables inside a recorded pane are ignored, with a warning.
func (r *run) who(as string) (core.Who, error) {
	w, err := r.c.Identify(r.ctx, r.env.Vars, r.env.Self)
	if err == nil {
		for _, v := range r.env.Vars {
			if strings.HasPrefix(v, "BS_") {
				k, _, _ := strings.Cut(v, "=")
				fmt.Fprintf(r.err, "bsx: ignoring %s inside %s's recorded pane\n", k, w.Name)
			}
		}
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

var commands = map[string]func(r *run, fs *flag.FlagSet, args []string) error{
	"spawn":  (*run).spawn,
	"done":   (*run).done,
	"end":    (*run).end,
	"prune":  (*run).prune,
	"send":   (*run).send,
	"inbox":  (*run).inbox,
	"status": (*run).status,
}
