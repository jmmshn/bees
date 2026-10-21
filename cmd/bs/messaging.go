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

// skipsFiles lists the commands that read only the environment, never the config files,
// while BS_LOGBOOK_DIR is set: a keeper editing config.toml mid-run can't silence a fleet
// whose workers carry the store in their env (ADR-0008). Every other command reads them.
var skipsFiles = []string{"send", "inbox", "status", "bead", "epic", "version"}

// settings loads the config for project name (none: "") as command cmd reads it.
func settings(cmd, name string) (*config.Loaded, error) {
	if slices.Contains(skipsFiles, cmd) && os.Getenv("BS_LOGBOOK_DIR") != "" {
		return config.Env()
	}
	return config.Load(name)
}

// resolve resolves the project, then loads its config as command cmd reads it and
// locates the project's files from that and flagRepo (--repo; "" if none).
func resolve(cmd, flagProject, flagRepo string) (project.Project, *config.Loaded, error) {
	name, err := project.Name(flagProject)
	if err != nil {
		return project.Project{}, nil, err
	}
	cfg, err := settings(cmd, name)
	if err != nil {
		return project.Project{Name: name}, nil, err
	}
	p, err := locate(name, cfg, flagRepo)
	return p, cfg, err
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
func scope(ctx context.Context, cmd, flagProject, flagRepo string, starts io.Writer) (project.Project, *logbook.Logbook, error) {
	p, cfg, err := resolve(cmd, flagProject, flagRepo)
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
	_, lb, err := scope(ctx, "init", *proj, "", stderr)
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
	from, err := project.Me(*as)
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
	p, lb, err := scope(ctx, "send", *proj, "", nil)
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
	id, err := lb.Send(ctx, from, to, body)
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
	session, err := project.Me(*as)
	if err != nil {
		return err
	}
	_, lb, err := scope(ctx, "inbox", *proj, "", nil)
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
	}
	if *peek {
		return nil
	}
	return lb.RecordReceipts(ctx, session, ids)
}
