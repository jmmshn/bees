package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/jmmshn/peons/internal/harness/claude"
	"github.com/jmmshn/peons/internal/peon"
	"github.com/jmmshn/peons/internal/project"
)

// newFleet builds the Fleet for a command. Tests swap in fakes.
var newFleet = func(f peon.Fleet) peon.Fleet {
	f.H, f.B = peon.HerdrCLI{}, peon.BeadsCLI{}
	return f
}

// fleet resolves the project, the calling session and the logbook into a Fleet.
// The caller closes the returned Fleet's logbook.
func fleet(ctx context.Context, flagProject, flagAs, flagRepo string) (peon.Fleet, error) {
	p, lb, err := scope(ctx, flagProject)
	if err != nil {
		return peon.Fleet{}, err
	}
	me, err := project.Me(flagAs)
	if err != nil {
		lb.Close()
		return peon.Fleet{}, err
	}
	f := peon.Fleet{P: p, LB: lb, Me: me}
	if flagRepo != "" {
		f.P.Repo, f.ForeignRepo = flagRepo, true
	}
	return newFleet(f), nil
}

func cmdSpawn(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("spawn", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: PNS_PROJECT, else the current git repo)")
	as := fs.String("as", "", "session doing the spawning (default: this Claude session)")
	repo := fs.String("repo", "", "spawn in this repo even if the bead belongs to another project")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "usage: pns spawn [--repo <path>] <bead>")
		return errUsage
	}
	f, err := fleet(ctx, *proj, *as, *repo)
	if err != nil {
		return err
	}
	defer f.LB.Close()
	if !claude.Trusted(f.P.Repo) {
		fmt.Fprintf(stderr, "pns spawn: Claude hasn't trusted %s yet; the peon will wait at the trust prompt until you answer it in its pane\n", f.P.Repo)
	}
	st, err := f.Spawn(ctx, pos[0])
	if err != nil {
		return err
	}
	if st.Resumed {
		fmt.Fprintf(stdout, "resumed %s in workspace %s\n", st.Name, st.Workspace)
	} else {
		fmt.Fprintf(stdout, "spawned %s for %s in workspace %s\n", st.Name, pos[0], st.Workspace)
	}
	if st.Waiting {
		fmt.Fprintf(stdout, "%s is waiting at a prompt (probably folder trust); answer it in its pane\n", st.Name)
	}
	return nil
}

func cmdEnd(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("end", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: PNS_PROJECT, else the current git repo)")
	as := fs.String("as", "", "session doing the ending (default: this Claude session)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "usage: pns end <peon>")
		return errUsage
	}
	f, err := fleet(ctx, *proj, *as, "")
	if err != nil {
		return err
	}
	defer f.LB.Close()
	kept, err := f.End(ctx, pos[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "ended %s\n", pos[0])
	if kept {
		fmt.Fprintf(stdout, "kept branch %s: it has work not merged yet\n", pos[0])
	}
	return nil
}
