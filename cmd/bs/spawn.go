package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/bee"
	"github.com/jmmshn/bees/internal/harness/claude"
	"github.com/jmmshn/bees/internal/project"
)

// newFleet builds the Fleet for a command. Tests swap in fakes.
var newFleet = func(f bee.Fleet) bee.Fleet {
	f.H, f.S, f.B, f.G = bee.HerdrCLI{}, bee.ClaudeSessions{}, bee.BeadsCLI{}, bee.GitHubCLI{}
	return f
}

// fleet resolves the project, the calling session and the logbook into a Fleet.
// The caller closes the returned Fleet's logbook.
func fleet(ctx context.Context, flagProject, flagAs, flagRepo string) (bee.Fleet, error) {
	p, lb, err := scope(ctx, flagProject)
	if err != nil {
		return bee.Fleet{}, err
	}
	me, err := project.Me(flagAs)
	if err != nil {
		lb.Close()
		return bee.Fleet{}, err
	}
	f := bee.Fleet{P: p, LB: lb, Me: me}
	if flagRepo != "" {
		f.P.Repo, f.ForeignRepo = flagRepo, true
	}
	return newFleet(f), nil
}

func cmdSpawn(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("spawn", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	as := fs.String("as", "", "session doing the spawning (default: this Claude session)")
	var reqs []bee.Request
	repo := ""
	fs.Func("repo", "spawn in this repo even if the beads belong to another project; before the first bead", func(s string) error {
		if len(reqs) > 0 {
			return errors.New("--repo goes before the first bead: one repo per call")
		}
		repo = s
		return nil
	})
	label, labelled := "", false // the --label waiting for its bead
	fs.Func("label", "the herdr workspace label of the bead after it: 1-4 lowercase words naming the work (required on a first spawn)", func(s string) error {
		if labelled {
			return fmt.Errorf("%q and %q are both waiting for one bead", label, s)
		}
		label, labelled = s, true
		return nil
	})
	spawnUsage := func(err error) error {
		if err != nil {
			fmt.Fprintf(stderr, "bs spawn: %v\n", err)
		}
		fmt.Fprintf(stderr, "usage: bs spawn --label \"<words>\" <bead> [--label \"<words>\" <bead> ...] [--repo <path>]\n%s\n", bee.LabelRules)
		return errUsage
	}
	// Like parseFlags, but each --label goes with the bead after it.
	var tail []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, tail = args[:i], args[i+1:]
	}
	addBead := func(b string) {
		reqs = append(reqs, bee.Request{Bead: b, Label: label})
		label, labelled = "", false
	}
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return errHelp
			}
			return errUsage
		}
		if args = fs.Args(); len(args) == 0 {
			break
		}
		addBead(args[0])
		args = args[1:]
	}
	for _, b := range tail {
		addBead(b)
	}
	if len(reqs) == 0 {
		return spawnUsage(nil)
	}
	if labelled {
		return spawnUsage(fmt.Errorf("--label %q has no bead after it", label))
	}
	for _, r := range reqs {
		if r.Label != "" {
			if err := bee.CheckLabel(r.Label); err != nil {
				return spawnUsage(err)
			}
		}
	}
	f, err := fleet(ctx, *proj, *as, repo)
	if err != nil {
		return err
	}
	defer f.LB.Close()
	trusted := claude.Trusted(f.P.Repo) // before spawning: starting a bee can't change it
	var untrusted []string
	failed, refused := false, false
	for _, r := range f.SpawnAll(ctx, reqs) {
		st := r.Started
		if r.Err != nil {
			fmt.Fprintf(stderr, "bs spawn: %s: %v\n", r.Bead, r.Err)
			failed, refused = true, refused || bee.Refused(r.Err)
			continue
		}
		took := r.Took.Round(100 * time.Millisecond).Seconds()
		switch {
		case st.Continued:
			fmt.Fprintf(stdout, "resumed %s (%s) in workspace %s (%.1f s)\n", st.Name, st.Label, st.Workspace, took)
		case st.Resumed:
			fmt.Fprintf(stdout, "restarted %s (%s) in workspace %s with a new conversation: it never read its brief (%.1f s)\n", st.Name, st.Label, st.Workspace, took)
		default:
			fmt.Fprintf(stdout, "spawned %s (%s) for %s in workspace %s (%.1f s)\n", st.Name, st.Label, r.Bead, st.Workspace, took)
		}
		switch {
		case st.Waiting:
			fmt.Fprintf(stdout, "%s is waiting at a prompt (probably folder trust); answer it in its pane\n", st.Name)
		case !trusted && !st.Continued: // a fresh restart may meet the prompt its first start did
			untrusted = append(untrusted, st.Name)
		}
	}
	if len(untrusted) > 0 {
		fmt.Fprintf(stderr, "bs spawn: Claude hasn't trusted %s yet; if %s asks, answer the trust prompt in its pane\n", f.P.Repo, strings.Join(untrusted, " or "))
	}
	switch {
	case refused: // nothing started
		return spawnUsage(nil)
	case failed:
		return exitCode(1)
	}
	return nil
}

func cmdEnd(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("end", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	as := fs.String("as", "", "session doing the ending (default: this Claude session)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "usage: bs end <bee>")
		return errUsage
	}
	f, err := fleet(ctx, *proj, *as, "")
	if err != nil {
		return err
	}
	defer f.LB.Close()
	if err := f.End(ctx, pos[0]); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "ended %s\nbranch %s kept; bs prune deletes it once merged\n", pos[0], pos[0])
	return nil
}

// Exit codes of bs prune when it keeps a branch.
const (
	exitKeptUnmerged = 3 // some branch has work not on the default branch
	exitKeptUnknown  = 4 // some branch could not be checked, or not deleted
)

func cmdPrune(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	dry := fs.Bool("dry-run", false, "report what would be deleted; delete nothing")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		fmt.Fprintln(stderr, "usage: bs prune [--dry-run]")
		return errUsage
	}
	p, lb, err := scope(ctx, *proj)
	if err != nil {
		return err
	}
	defer lb.Close()
	pruned, err := newFleet(bee.Fleet{P: p, LB: lb}).Prune(ctx, *dry)
	if err != nil {
		return err
	}
	if len(pruned) == 0 {
		fmt.Fprintln(stdout, "nothing to prune: no ended bee has a branch left")
		return nil
	}
	code := 0
	for _, r := range pruned {
		switch {
		case r.Merge == bee.Merged && *dry:
			fmt.Fprintf(stdout, "would delete %s: merged, %s\n", r.Branch, r.Why)
		case r.Deleted:
			fmt.Fprintf(stdout, "deleted %s: merged, %s\n", r.Branch, r.Why)
		case r.Merge == bee.Merged:
			fmt.Fprintf(stdout, "kept %s: merged, but deleting it failed: %v\n", r.Branch, r.DeleteErr)
			code = exitKeptUnknown
		case r.Merge == bee.NotMerged:
			fmt.Fprintf(stdout, "kept %s: not merged: %s\n", r.Branch, r.Why)
			code = max(code, exitKeptUnmerged)
		default:
			fmt.Fprintf(stdout, "kept %s: could not check: %s\n", r.Branch, r.Why)
			code = exitKeptUnknown
		}
	}
	if code != 0 {
		return exitCode(code)
	}
	return nil
}
