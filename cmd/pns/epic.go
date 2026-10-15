package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/jmmshn/peons/internal/peon"
	"github.com/jmmshn/peons/internal/project"
)

func cmdEpic(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("epic", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: PNS_PROJECT, else the current git repo)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		fmt.Fprintln(stderr, "usage: pns epic <title>")
		return errUsage
	}
	p, err := project.Resolve(*proj)
	if err != nil {
		return err
	}
	ids, err := peon.BeadsCLI{}.IDs(ctx)
	if err != nil {
		return err
	}
	id := peon.NextEpic(p.Name, ids)
	if err := (peon.BeadsCLI{}).CreateEpic(ctx, id, strings.Join(pos, " ")); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s\nadd beads with: bd create --parent %s --title \"...\"\n", id, id)
	return nil
}
