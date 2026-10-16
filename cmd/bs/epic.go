package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/jmmshn/bees/internal/bee"
	"github.com/jmmshn/bees/internal/project"
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
	ids, err := bee.BeadsCLI{}.IDs(ctx)
	if err != nil {
		return err
	}
	id := bee.NextEpic(p.Name, ids)
	if err := (bee.BeadsCLI{}).CreateEpic(ctx, id, strings.Join(pos, " ")); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s\nadd beads with: bd create --parent %s --title \"...\"\n", id, id)
	return nil
}
