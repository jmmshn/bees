package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/harness/claude"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/project"
)

// errUsage means the arguments were wrong; the message has already been printed.
var errUsage = errors.New("usage")

// scope resolves the project and opens its logbook. Callers close the logbook.
func scope(ctx context.Context, flagProject string) (project.Project, *logbook.Logbook, error) {
	p, err := project.Resolve(flagProject)
	if err != nil {
		return p, nil, err
	}
	lb, err := logbook.Open(ctx, p.Logbook)
	return p, lb, err
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
	_, lb, err := scope(ctx, *proj)
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
	_, lb, err := scope(ctx, *proj)
	if err != nil {
		return err
	}
	defer lb.Close()
	id, err := lb.Send(ctx, from, to, body)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "sent %s to %s\n", id, to)
	return ring(ctx, lb, from, to, *now, stdout, stderr)
}

// ring rings to with its unread count. The message is already stored, so a failed ring
// is only a warning: to reads it on its next bs inbox.
func ring(ctx context.Context, lb *logbook.Logbook, from, to string, now bool, stdout, stderr io.Writer) error {
	err := ringOnce(ctx, lb, from, to, now)
	switch {
	case errors.Is(err, claude.ErrNotRunning):
		fmt.Fprintf(stdout, "queued; %s is not running\n", to)
	case err != nil:
		fmt.Fprintf(stderr, "bs send: couldn't ring %s (the message is stored): %v\n", to, err)
	}
	return nil
}

func ringOnce(ctx context.Context, lb *logbook.Logbook, from, to string, now bool) error {
	s, err := claude.Find(to)
	if err != nil {
		return err
	}
	unread, err := lb.Unread(ctx, to)
	if err != nil {
		return err
	}
	priority := claude.Next
	if now {
		priority = claude.Now
	}
	return claude.Ring(s, doorbell(unread, from), priority)
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
	_, lb, err := scope(ctx, *proj)
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
