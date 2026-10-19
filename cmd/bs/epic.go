package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/jmmshn/bees/internal/project"
	"github.com/jmmshn/bees/internal/worker"
)

func cmdEpic(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("epic", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		fmt.Fprintln(stderr, "usage: bs epic <title>")
		return errUsage
	}
	p, err := project.Resolve(*proj)
	if err != nil {
		return err
	}
	ids, err := worker.BeadsCLI{}.IDs(ctx)
	if err != nil {
		return err
	}
	id := worker.NextEpic(p.Name, ids)
	if err := (worker.BeadsCLI{}).CreateEpic(ctx, id, strings.Join(pos, " ")); err != nil {
		return err
	}
	fmt.Fprintln(stdout, id)
	fmt.Fprintf(stderr, "add beads with: bs bead new --parent %s \"...\"\n", id)
	return nil
}
