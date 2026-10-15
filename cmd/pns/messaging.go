package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmmshn/peons/internal/logbook"
)

// errUsage means the arguments were wrong; the message has already been printed.
var errUsage = errors.New("usage")

func openLogbook(ctx context.Context) (*logbook.Logbook, error) {
	path := os.Getenv("PNS_LOGBOOK")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".local", "share", "peons", "logbook.db")
	}
	return logbook.Open(ctx, path)
}

// me resolves which session is running pns: --as, then PNS_SESSION.
func me(as string, stderr io.Writer) (string, error) {
	if as == "" {
		as = os.Getenv("PNS_SESSION")
	}
	if as == "" {
		fmt.Fprintln(stderr, "pns: can't tell which session you are; pass --as <session> or set PNS_SESSION")
		return "", errUsage
	}
	return as, nil
}

func cmdInit(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	lb, err := openLogbook(ctx)
	if err != nil {
		return err
	}
	defer lb.Close()
	return lb.Init(ctx)
}

func cmdSend(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	as := fs.String("as", "", "session sending the message")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: pns send [--as <session>] <to> <body>")
		return errUsage
	}
	from, err := me(*as, stderr)
	if err != nil {
		return err
	}
	to, body := fs.Arg(0), strings.Join(fs.Args()[1:], " ")
	lb, err := openLogbook(ctx)
	if err != nil {
		return err
	}
	defer lb.Close()
	id, err := lb.Send(ctx, from, to, body)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "sent %s to %s\n", id, to)
	return nil
}

func cmdInbox(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	fs.SetOutput(stderr)
	as := fs.String("as", "", "session whose inbox to show")
	peek := fs.Bool("peek", false, "show unread messages without recording receipts")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	session, err := me(*as, stderr)
	if err != nil {
		return err
	}
	lb, err := openLogbook(ctx)
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
	return lb.MarkRead(ctx, session, ids)
}
