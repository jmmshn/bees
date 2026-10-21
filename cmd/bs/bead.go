package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/jmmshn/bees/internal/worker"
)

const beadUsage = `usage: bs bead ls [--all] [<bd query expression>]
       bs bead new --parent <project>-N [--description <text>] [--type <type>] [--priority <0-4>] <title>`

// cmdBead lists and creates the project's beads, so the queen and workers never step
// outside the project (R32).
func cmdBead(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, beadUsage)
		return errUsage
	}
	switch args[0] {
	case "ls":
		return beadLs(ctx, args[1:], stdout, stderr)
	case "new":
		return beadNew(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, beadUsage)
		return errUsage
	}
}

func beadLs(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("bead ls", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	all := fs.Bool("all", false, "include closed beads")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	p, _, err := resolve("bead", *proj, "")
	if err != nil {
		return err
	}
	expr := fmt.Sprintf("id=%q", p.Name+"-*")
	if !*all {
		expr += " AND status!=closed"
	}
	if len(pos) > 0 {
		expr += " AND (" + strings.Join(pos, " ") + ")"
	}
	// Project names are only letters and digits, so the id wildcard can't match another
	// project's beads.
	cmd := exec.CommandContext(ctx, "bd", "query", "--limit", "0", expr)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

func beadNew(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("bead new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	parent := fs.String("parent", "", "the project bead to file under, <project>-N[.M…] (required)")
	desc := fs.String("description", "", "description")
	typ := fs.String("type", "", "bd issue type (default: bd's, task)")
	prio := fs.String("priority", "", "priority 0-4 (default: bd's, 2)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		fmt.Fprintln(stderr, beadUsage)
		return errUsage
	}
	p, _, err := resolve("bead", *proj, "")
	if err != nil {
		return err
	}
	if *parent == "" {
		fmt.Fprintf(stderr, "bs bead new: needs --parent %s-N; a project's top-level beads are epics: bs epic \"<title>\"\n", p.Name)
		return errUsage
	}
	if !worker.Belongs(p.Name, *parent) {
		return fmt.Errorf("%s is not a %s bead (want %s-N[.M…])", *parent, p.Name, p.Name)
	}
	id, err := worker.BeadsCLI{}.Create(ctx, *parent, strings.Join(pos, " "), *desc, *typ, *prio)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, id)
	return nil
}
