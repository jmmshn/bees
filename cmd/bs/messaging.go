package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/project"
	"github.com/jmmshn/bees/internal/worker"
)

// errUsage means the arguments were wrong; the message has already been printed.
var errUsage = errors.New("usage")

// errNoLogbook means the project's logbook is missing and the command may not start one.
var errNoLogbook = errors.New("no logbook")

// skipsFiles lists the commands that, in a recorded herdr pane, read only the global
// file and skip the project's (ADR-0008, ADR-0009 amendment 1): a keeper editing a
// project file mid-run can't silence a fleet. A global file they can't read falls back to
// the defaults, with a warning. Every other command, and every command outside a recorded
// pane, reads both files.
var skipsFiles = []string{"send", "inbox", "status", "bead", "epic", "version"}

// settings loads the config for project name (none: "") as command cmd reads it.
func settings(cmd, name string, c caller, stderr io.Writer) (*config.Loaded, error) {
	if !c.inPane || !slices.Contains(skipsFiles, cmd) {
		return config.Load(name)
	}
	l, err := config.Load("")
	if err != nil {
		fmt.Fprintf(warn(stderr), "bs %s: warning: %v; using the defaults\n", cmd, err)
		return config.Env()
	}
	return l, nil
}

// resolve resolves the project, from the caller's herdr pane when an entry recorded it,
// then loads its config as command cmd reads it and locates the project's files from that
// and flagRepo (--repo; "" if none).
func resolve(ctx context.Context, cmd, flagProject, flagRepo string, stderr io.Writer) (project.Project, *config.Loaded, error) {
	c, err := whoCalls(ctx, stderr)
	if err != nil {
		return project.Project{}, nil, err
	}
	name, err := c.projectFor(cmd, flagProject, stderr)
	if err != nil {
		return project.Project{}, nil, err
	}
	cfg, err := settings(cmd, name, c, stderr)
	if err != nil {
		return project.Project{Name: name}, nil, err
	}
	p, err := locate(name, cfg, flagRepo)
	c.setLogbook(&p, stderr)
	return p, cfg, err
}

// atFleetLogbook refuses (exit 2) a project whose logbook is not <global
// paths.logbook_dir>/<project>.db: its sessions' panes would never be found there.
func atFleetLogbook(cmd string, p project.Project, stderr io.Writer) error {
	dir, err := globalLogbookDir()
	if err != nil {
		return err
	}
	if want := filepath.Join(dir, p.Name+".db"); filepath.Clean(p.Logbook) != want {
		fmt.Fprintf(stderr, "bs %s: project %s's logbook is %s, not %s: the sessions bs starts are found by their herdr pane in the global paths.logbook_dir only; unset BS_LOGBOOK and BS_LOGBOOK_DIR, or set paths.logbook_dir in the global config\n", cmd, p.Name, p.Logbook, want)
		return exitCode(2)
	}
	return nil
}

// locate finds project name's files from its loaded config and flagRepo.
func locate(name string, cfg *config.Loaded, flagRepo string) (project.Project, error) {
	return project.Locate(name, project.Paths{
		LogbookDir: config.Expand(cfg.Paths.LogbookDir),
		RepoFlag:   flagRepo,
		Repo:       config.Expand(cfg.Paths.Repo),
		Repos:      config.Expand(cfg.Paths.Repos),
		Worktrees:  config.Expand(cfg.Paths.Worktrees),
		AnyVCS:     cfg.Workspace.Kind == worker.NoWorkspace,
	})
}

// scope resolves the project as command cmd does (with --repo flagRepo) and opens its logbook. Callers close
// the logbook. Only a command that starts a project (init, queen, spawn) passes starts,
// where a new logbook is announced; with nil a missing logbook is refused, since opening
// one creates it and a deleted logbook would silently come back empty (bees-8.12, motif
// 14). A missing logbook is refused either way when paths.logbook_dir moved away from one
// that exists: starting afresh would split the store.
func scope(ctx context.Context, cmd, flagProject, flagRepo string, starts, stderr io.Writer) (project.Project, *logbook.Logbook, error) {
	p, cfg, err := resolve(ctx, cmd, flagProject, flagRepo, stderr)
	if err != nil {
		return p, nil, err
	}
	if _, err := os.Stat(p.Logbook); errors.Is(err, fs.ErrNotExist) {
		if err := moved(p, cfg); err != nil {
			return p, nil, err
		}
		if starts == nil {
			return p, nil, fmt.Errorf("%w for project %s at %s: start the project with bs queen; "+
				"if it already had a queen or workers, the logbook was deleted, so restore the file "+
				"rather than starting afresh", errNoLogbook, p.Name, p.Logbook)
		}
		fmt.Fprintf(starts, "bs: new empty logbook for project %s at %s\n", p.Name, p.Logbook)
	} else if err != nil {
		return p, nil, err
	}
	lb, err := logbook.Open(ctx, p.Logbook)
	return p, lb, err
}

// moved refuses a missing logbook when a value paths.logbook_dir overrides (the default,
// or a file's) holds the project's logbook. BS_LOGBOOK names the file outright: no move.
func moved(p project.Project, cfg *config.Loaded) error {
	if os.Getenv("BS_LOGBOOK") != "" {
		return nil
	}
	for _, dir := range cfg.Shadowed["paths.logbook_dir"] {
		old := filepath.Join(config.Expand(dir), p.Name+".db")
		if _, err := os.Stat(old); err == nil && old != p.Logbook {
			return fmt.Errorf("%w for project %s at %s, but paths.logbook_dir moved there (%s) from %s, which has one: "+
				"move %s into %s, or set paths.logbook_dir back", errNoLogbook, p.Name, p.Logbook, cfg.Sources["paths.logbook_dir"], dir, old, filepath.Dir(p.Logbook))
		}
	}
	return nil
}

// parseFlags parses fs's flags wherever they appear among the arguments and returns
// the positional arguments. Everything after "--" is positional.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, errHelp
			}
			return nil, errUsage
		}
		args = fs.Args()
		if len(args) == 0 {
			return append(pos, tail...), nil
		}
		pos, args = append(pos, args[0]), args[1:]
	}
}

// errHelp means -h was asked for; the help has already been printed.
var errHelp = errors.New("help")

func cmdInit(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "usage: bs init [--project <name>]")
		return errUsage
	}
	_, lb, err := scope(ctx, "init", *proj, "", stderr, stderr)
	if err != nil {
		return err
	}
	return lb.Close()
}

func cmdSend(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	as := fs.String("as", "", "session sending the message")
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	now := fs.Bool("now", false, "ring with priority now: interrupt the recipient's current turn")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		fmt.Fprintln(stderr, "usage: bs send [--as <session>] [--now] <to> <body | ->")
		return errUsage
	}
	from, c, err := me(ctx, *as, stderr)
	if err != nil {
		return err
	}
	to, body := pos[0], strings.Join(pos[1:], " ")
	if body == "-" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		body = strings.TrimRight(string(b), "\n")
	}
	p, lb, err := scope(ctx, "send", *proj, "", nil, stderr)
	if err != nil {
		return err
	}
	defer lb.Close()
	if to, err = worker.Address(ctx, lb, p.Name, to); err != nil {
		return err
	}
	if err := project.CheckSession(to); err != nil {
		return err
	}
	fromProject := "" // the logbook's own project
	if c.inPane && c.Project != p.Name {
		fromProject = c.Project // cross-project mail: the reply goes back to the pane's project
	}
	id, err := lb.SendFrom(ctx, from, fromProject, to, body)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "sent %s to %s\n", id, to)
	if stranger, err := warnStranger(ctx, lb, p, to, stderr); err != nil || stranger {
		return err
	}
	return ring(ctx, lb, from, to, *now, stdout, stderr)
}

// warnStranger warns when to is neither p's queen nor a worker spawned in p: it reads
// another project's logbook, so the message may never be read (bees-1.54). A stranger is
// never rung: a ring is a user turn, and bs rings only its own project's sessions (bees-7.4).
func warnStranger(ctx context.Context, lb *logbook.Logbook, p project.Project, to string, stderr io.Writer) (bool, error) {
	if to == p.Queen() {
		return false, nil
	}
	if _, ok, err := lb.LatestSpawn(ctx, to); err != nil || ok {
		return false, err
	}
	fmt.Fprintf(stderr, "bs send: warning: %s is not %s or a worker spawned in project %s, so bs stored the message without ringing it; %s reads this logbook (%s) only if it runs in %s (set BS_PROJECT or pass --project)\n", to, p.Queen(), p.Name, to, p.Logbook, p.Name)
	return true, nil
}

// ring rings to with its unread count. The message is already stored, so a failed ring
// is only a warning: to reads it on its next bs inbox.
func ring(ctx context.Context, lb *logbook.Logbook, from, to string, now bool, stdout, stderr io.Writer) error {
	err := ringOnce(ctx, lb, from, to, now)
	switch {
	case errors.Is(err, harness.ErrNotRunning):
		fmt.Fprintf(stdout, "queued; %s is not running\n", to)
	case errors.Is(err, harness.ErrNoRing):
		fmt.Fprintf(stdout, "stored; %s's harness has no ring yet, so it reads this at its next bs inbox\n", to)
	case err != nil:
		fmt.Fprintf(stderr, "bs send: couldn't ring %s (the message is stored): %v\n", to, err)
	}
	return nil
}

// ringOnce rings to through the harness its spawn recorded; the queen, who has no record
// yet, through harness.Default.
func ringOnce(ctx context.Context, lb *logbook.Logbook, from, to string, now bool) error {
	name := harness.Default
	if sp, ok, err := lb.LatestSpawn(ctx, to); err != nil {
		return err
	} else if ok {
		name = sp.Harness
	}
	h, err := harnessFor(name)
	if err != nil {
		return err
	}
	unread, err := lb.Unread(ctx, to)
	if err != nil {
		return err
	}
	priority := harness.Next
	if now {
		priority = harness.Now
	}
	return h.Ring(harness.Target{Name: to}, doorbell(unread, from), priority)
}

// doorbell names who the unread messages are from, never what they say:
// [bs] 3 unread (queen ×2, bees-1-2) — run: bs inbox   (reply with: bs send queen "...")
func doorbell(unread []logbook.Message, from string) string {
	var senders []string
	count := map[string]int{}
	for _, m := range unread {
		if count[m.From] == 0 {
			senders = append(senders, m.From)
		}
		count[m.From]++
	}
	for i, s := range senders {
		if count[s] > 1 {
			senders[i] = fmt.Sprintf("%s ×%d", s, count[s])
		}
	}
	return fmt.Sprintf(`[bs] %d unread (%s) — run: bs inbox   (reply with: bs send %s "...")`, len(unread), strings.Join(senders, ", "), from)
}

func cmdInbox(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	fs.SetOutput(stderr)
	as := fs.String("as", "", "session whose inbox to show")
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	peek := fs.Bool("peek", false, "show unread messages without recording receipts")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "usage: bs inbox [--as <session>] [--peek]")
		return errUsage
	}
	session, _, err := me(ctx, *as, stderr)
	if err != nil {
		return err
	}
	_, lb, err := scope(ctx, "inbox", *proj, "", nil, stderr)
	if err != nil {
		return err
	}
	defer lb.Close()
	msgs, err := lb.Unread(ctx, session)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		fmt.Fprintln(stdout, "no unread messages")
		return nil
	}
	ids := make([]logbook.ID, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
		fmt.Fprintf(stdout, "── %s from %s · %s\n%s\n\n", m.ID, m.From, m.TS.Local().Format(time.DateTime), m.Body)
		if m.FromProject != "" { // cross-project mail: the sender reads its own project's logbook
			fmt.Fprintf(stdout, "   (reply with: bs send --project %s %s \"...\")\n\n", m.FromProject, m.From)
		}
	}
	if *peek {
		return nil
	}
	return lb.RecordReceipts(ctx, session, ids)
}
