package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/identity"
	"github.com/jmmshn/bees/internal/project"
)

// caller is who runs this bs: the session its herdr pane names, when an entry recorded
// that pane and its process is this bs's ancestor (ADR-0009 amendment 1). Inside such a
// pane, the pane decides the project, the name and the logbook; outside one, --project,
// BS_PROJECT and git, then --as, the Claude session and BS_SESSION decide, as before.
type caller struct {
	identity.Session
	inPane bool
	dir    string // the global logbook dir the scan read
}

// whoCalls finds the caller, reading only the global file's paths.logbook_dir: never a
// project file, never env. A global file bs can't read falls back to the default dir,
// with a warning: a pane found there is right, and one found nowhere names no one. It
// scans once per command (run puts a memo in ctx), so the steps of one command can't
// disagree about who is calling (motif 2).
func whoCalls(ctx context.Context, stderr io.Writer) (caller, error) {
	m, _ := ctx.Value(memoKey{}).(*memo)
	if m != nil && m.done {
		return m.c, m.err
	}
	c, err := findCaller(ctx, stderr)
	if m != nil {
		m.c, m.err, m.done = c, err, true
	}
	return c, err
}

// memo holds a command's caller once whoCalls found it.
type memo struct {
	c    caller
	err  error
	done bool
}

type memoKey struct{}

// withMemo is ctx for one command.
func withMemo(ctx context.Context) context.Context { return context.WithValue(ctx, memoKey{}, &memo{}) }

func findCaller(ctx context.Context, stderr io.Writer) (caller, error) {
	pane := os.Getenv("HERDR_PANE_ID")
	if pane == "" {
		return caller{}, nil
	}
	dir, err := globalLogbookDir()
	if err != nil {
		dir = config.Expand(defaultLogbookDir)
		fmt.Fprintf(warn(stderr), "bs: warning: %v; looking for this pane's session in %s\n", err, dir)
	}
	s, ok, err := identity.Find(ctx, dir, pane, kindOf)
	return caller{Session: s, inPane: ok, dir: dir}, err
}

// defaultLogbookDir is paths.logbook_dir's default.
const defaultLogbookDir = "~/.local/share/bees"

// globalLogbookDir is the global file's paths.logbook_dir (or its default), before any
// BS_LOGBOOK_DIR: where every recorded pane's logbook lives.
func globalLogbookDir() (string, error) {
	l, err := config.Load("")
	if err != nil {
		return "", err
	}
	return config.Expand(l.FileValue("paths.logbook_dir")), nil
}

// kindOf is the process name a recorded harness runs as; "" for one this bs doesn't know.
func kindOf(name string) string {
	h, err := harnessFor(name)
	if err != nil {
		return ""
	}
	return h.Kind()
}

// warn is stderr, or nowhere when a caller has none.
func warn(stderr io.Writer) io.Writer {
	if stderr == nil {
		return io.Discard
	}
	return stderr
}

// projectFor is the project command cmd acts in. Inside a recorded pane it is the pane's,
// and an inherited BS_PROJECT that disagrees is ignored, with a warning. A --project
// naming another project is the recipient's logbook for send and is read by the
// read-only commands, but inbox acts only in the pane's own project.
func (c caller) projectFor(cmd, flag string, stderr io.Writer) (string, error) {
	if !c.inPane {
		return project.Name(flag)
	}
	if env := os.Getenv("BS_PROJECT"); env != "" && project.Normalise(env) != c.Project {
		fmt.Fprintf(warn(stderr), "bs: warning: ignoring BS_PROJECT=%s: this herdr pane is %s's, in project %s\n", env, c.Name, c.Project)
	}
	if flag == "" || project.Normalise(flag) == c.Project {
		return c.Project, nil
	}
	if cmd == "inbox" {
		return "", fmt.Errorf("--project %s: %s reads only its own project's inbox (%s); drop --project", flag, c.Name, c.Project)
	}
	return project.Name(flag)
}

// setLogbook points p at its logbook as the pane sees it: the one that recorded the pane
// for the pane's own project, <global paths.logbook_dir>/<project>.db for another (the
// recipient of a cross-project send). An inherited BS_LOGBOOK or BS_LOGBOOK_DIR that
// disagrees is ignored, with a warning.
func (c caller) setLogbook(p *project.Project, stderr io.Writer) {
	if !c.inPane {
		return
	}
	lb := filepath.Join(c.dir, p.Name+".db")
	if p.Name == c.Project {
		lb = c.Logbook
	}
	if env := os.Getenv("BS_LOGBOOK"); env != "" && filepath.Clean(env) != lb {
		fmt.Fprintf(warn(stderr), "bs: warning: ignoring BS_LOGBOOK=%s: in this herdr pane, project %s's logbook is %s\n", env, p.Name, lb)
	}
	if env := os.Getenv("BS_LOGBOOK_DIR"); env != "" && filepath.Clean(config.Expand(env)) != filepath.Clean(c.dir) {
		fmt.Fprintf(warn(stderr), "bs: warning: ignoring BS_LOGBOOK_DIR=%s: in this herdr pane, logbooks are in %s\n", env, c.dir)
	}
	p.Logbook = lb
}

// me is the session running bs, and the caller it found. Inside a recorded pane it is the
// pane's, and an --as naming anyone else is refused (the sender's identity, bees-7.4); an
// inherited BS_SESSION that disagrees is ignored, with a warning. Outside one, project.Me
// decides.
func me(ctx context.Context, flag string, stderr io.Writer) (string, caller, error) {
	c, err := whoCalls(ctx, stderr)
	if err != nil {
		return "", c, err
	}
	if !c.inPane {
		name, err := project.Me(flag)
		return name, c, err
	}
	if flag != "" && flag != c.Name {
		return "", c, fmt.Errorf("--as %s: %w %q (its herdr pane)", flag, project.ErrNotYou, c.Name)
	}
	if env := os.Getenv("BS_SESSION"); env != "" && env != c.Name {
		fmt.Fprintf(warn(stderr), "bs: warning: ignoring BS_SESSION=%s: this herdr pane is %s's\n", env, c.Name)
	}
	return c.Name, c, nil
}
