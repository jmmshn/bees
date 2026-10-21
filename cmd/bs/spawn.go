package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/harness/claude"
	"github.com/jmmshn/bees/internal/worker"
)

// newProcs is the process table bs status reads. Tests swap in fakes.
var newProcs = func() worker.Procs { return worker.SystemProcs{} }

// newFleet builds the Fleet for a command. Tests swap in fakes.
var newFleet = func(f worker.Fleet) worker.Fleet {
	f.H, f.Procs, f.B, f.G = worker.HerdrCLI{}, worker.SystemProcs{}, worker.BeadsCLI{}, worker.GitHubCLI{}
	return f
}

// fleet resolves the project, the calling session and the logbook into a Fleet; starts is
// as for scope. The caller closes the returned Fleet's logbook.
func fleet(ctx context.Context, cmd, flagProject, flagAs, flagRepo string, starts, stderr io.Writer) (worker.Fleet, error) {
	if cmd == "spawn" { // before scope, which starts a missing logbook
		if p, _, err := resolve(ctx, cmd, flagProject, flagRepo, nil); err == nil { // scope warns
			if err := atFleetLogbook(cmd, p, stderr); err != nil {
				return worker.Fleet{}, err
			}
		}
	}
	p, lb, err := scope(ctx, cmd, flagProject, flagRepo, starts, stderr)
	if err != nil {
		return worker.Fleet{}, err
	}
	who, _, err := me(ctx, flagAs, stderr)
	if err != nil {
		lb.Close()
		return worker.Fleet{}, err
	}
	cfg, err := config.Load(p.Name)
	if err != nil {
		lb.Close()
		return worker.Fleet{}, err
	}
	h, err := adapter(cfg, cfg.Worker.Role())
	if err != nil {
		lb.Close()
		return worker.Fleet{}, err
	}
	return newFleet(worker.Fleet{P: p, LB: lb, Me: who, Harness: h, HarnessName: cfg.Harness, Delivery: cfg.Delivery, Guards: cfg.Guards, WorkspaceKind: cfg.Workspace.Kind, DefaultPath: expandAll(cfg.Worker.Path), ForeignRepo: flagRepo != ""}), nil
}

func cmdSpawn(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("spawn", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	as := fs.String("as", "", "session doing the spawning (default: this Claude session)")
	var reqs []worker.Request
	repo := ""
	fs.Func("repo", "spawn in this repo even if the beads belong to another project; before the first bead", func(s string) error {
		if len(reqs) > 0 {
			return errors.New("--repo goes before the first bead: one repo per call")
		}
		repo = s
		return nil
	})
	var path []string
	fs.Func("path", "put this dir first on the PATH of every worker's shell commands, e.g. a live test's build (repeatable; absolute); before the first bead", func(s string) error {
		if len(reqs) > 0 {
			return errors.New("--path goes before the first bead: it applies to every worker of the call")
		}
		path = append(path, s)
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
		fmt.Fprintf(stderr, "usage: bs spawn --label \"<words>\" <bead> [--label \"<words>\" <bead> ...] [--repo <path>] [--path <dir> ...]\n%s\n", worker.LabelRules)
		return errUsage
	}
	// Like parseFlags, but each --label goes with the bead after it.
	var tail []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, tail = args[:i], args[i+1:]
	}
	addBead := func(b string) {
		reqs = append(reqs, worker.Request{Bead: b, Label: label, Path: path})
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
			if err := worker.CheckLabel(r.Label); err != nil {
				return spawnUsage(err)
			}
		}
	}
	f, err := fleet(ctx, "spawn", *proj, *as, repo, stderr, stderr)
	if err != nil {
		return err
	}
	defer f.LB.Close()
	if err := f.P.NeedRepo(); err != nil {
		return err
	}
	trusted := claude.Trusted(f.P.Repo) // before spawning: starting a worker can't change it
	var untrusted []string
	failed, refused := false, false
	for _, r := range f.SpawnAll(ctx, reqs) {
		st := r.Started
		if r.Err != nil {
			fmt.Fprintf(stderr, "bs spawn: %s: %v\n", r.Bead, r.Err)
			failed, refused = true, refused || worker.Refused(r.Err)
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
		if st.Warn != nil {
			fmt.Fprintf(stderr, "bs spawn: warning: %s started, but herdr didn't take its name (the sidebar shows another): %v\n", st.Name, st.Warn)
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
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo; --all needs it)")
	as := fs.String("as", "", "session doing the ending (default: this Claude session)")
	var o worker.EndOpts
	fs.BoolVar(&o.Force, "force", false, "end it even while it runs background work, or with uncommitted or untracked files (discarding them)")
	fs.BoolVar(&o.Forget, "forget", false, "record the end and leave the workspace open: one herdr handed to another worktree, or one that holds another session's pane (only the worker's own pane is closed)")
	all := fs.Bool("all", false, "end every worker the project's logbook records as spawned and not yet ended")
	yes := fs.Bool("yes", false, "with --all: end them even while the project's queen is running, or from the queen itself")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if *all != (len(pos) == 0) || *all && *proj == "" {
		fmt.Fprintln(stderr, "usage: bs end [--force] [--forget] <worker | bead>\n       bs end --all --project <project> [--force] [--forget] [--yes]")
		return errUsage
	}
	f, err := fleet(ctx, "end", *proj, *as, "", nil, stderr)
	if err != nil {
		return err
	}
	defer f.LB.Close()
	if *all {
		if err := mayEndAll(ctx, f, *yes); err != nil {
			return err
		}
		return endAll(ctx, f, o, stdout, stderr)
	}
	name, err := worker.Address(ctx, f.LB, f.P.Name, pos[0])
	if err != nil {
		return err
	}
	e, err := f.EndOne(ctx, name, o)
	if code := gateCode(err); code != 0 {
		fmt.Fprintf(stderr, "bs end: %v\nbs end --force ends it anyway\n", err)
		return exitCode(code)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "ended %s\n", name)
	if e.Exited {
		fmt.Fprintf(stdout, noHarness+"\n", name)
	}
	if sp, _, err := f.LB.LatestSpawn(ctx, name); err == nil && sp.Branch != "" {
		fmt.Fprintf(stdout, "branch %s kept; bs prune deletes it once merged\n", sp.Branch)
	}
	return nil
}

// mayEndAll refuses an --all that would end the wrong fleet: one read from a BS_LOGBOOK that
// is not the project's, one run by a worker of the project (it would end itself and its
// peers), or, without yes, one run by the project's queen or while it is running.
func mayEndAll(ctx context.Context, f worker.Fleet, yes bool) error {
	if lb := os.Getenv("BS_LOGBOOK"); lb != "" && filepath.Base(lb) != f.P.Name+".db" {
		return fmt.Errorf("BS_LOGBOOK=%s is not project %s's logbook; unset it, or pass the project it belongs to", lb, f.P.Name)
	}
	if worker.IsWorkerOf(f.P.Name, f.Me) {
		return fmt.Errorf("%s is a worker of project %s: a worker never ends its whole project; ask the queen", f.Me, f.P.Name)
	}
	if yes {
		return nil
	}
	if f.Me == f.P.Queen() {
		return fmt.Errorf("this ends every worker of your own project %s; pass --yes if you mean it", f.P.Name)
	}
	switch alive, known, err := worker.QueenAlive(ctx, f.LB, f.Procs, f.P.Queen()); {
	case err != nil:
		return fmt.Errorf("can't tell whether %s is running (pass --yes to end its workers anyway): %w", f.P.Queen(), err)
	case !known: // fail closed: a queen started before bs recorded queens may be running (motif 7)
		return fmt.Errorf("can't tell whether %s is running: the logbook records no queen; pass --yes to end its workers anyway", f.P.Queen())
	case alive:
		return fmt.Errorf("%s is running and may spawn again; stop it first, or pass --yes", f.P.Queen())
	}
	return nil
}

// Exit codes of bs end --all when it keeps a worker.
const (
	exitEndKept      = 1 // some worker could not be ended
	exitEndHerdrDown = 3 // herdr didn't answer, so some worker's workspace may still be open
)

// endAll ends the project's live workers, one line each, and exits nonzero when it keeps
// any: a re-run ends what is left.
func endAll(ctx context.Context, f worker.Fleet, o worker.EndOpts, stdout, stderr io.Writer) error {
	ended, err := f.EndAll(ctx, o)
	if err != nil {
		return err
	}
	if len(ended) == 0 {
		fmt.Fprintf(stdout, "nothing to end: project %s has no worker spawned and not yet ended\n", f.P.Name)
		return nil
	}
	code := 0
	for _, e := range ended {
		switch {
		case gateCode(e.Err) != 0:
			fmt.Fprintf(stderr, "bs end: kept %v\n", e.Err)
			code = max(code, gateCode(e.Err))
		case errors.Is(e.Err, worker.ErrHerdrDown):
			fmt.Fprintf(stderr, "bs end: kept %v\n", e.Err)
			code = max(code, exitEndHerdrDown)
		case e.Err != nil:
			fmt.Fprintf(stderr, "bs end: kept %v\n", e.Err)
			code = max(code, exitEndKept)
		case e.Forgot:
			fmt.Fprintf(stdout, "ended %s: workspace %s is open on another worktree, left open\n", e.Name, e.Workspace)
		case e.Gone:
			fmt.Fprintf(stdout, "ended %s: workspace %s was already gone\n", e.Name, e.Workspace)
		default:
			fmt.Fprintf(stdout, "ended %s: removed workspace %s\n", e.Name, e.Workspace)
		}
		if e.Err == nil && e.Exited {
			fmt.Fprintf(stdout, "  "+noHarness+"\n", e.Name)
		}
		if e.Err == nil && e.Warn != nil {
			fmt.Fprintf(stderr, "bs end: %s ended, but its settings files may be left: %v\n", e.Name, e.Warn)
		}
	}
	if code != 0 {
		fmt.Fprintf(stderr, "bs end: some workers are still live; fix the cause above and run bs end --all --project %s again\n", f.P.Name)
		return exitCode(code)
	}
	if slices.ContainsFunc(ended, func(e worker.Ending) bool { return e.Branch != "" }) {
		fmt.Fprintf(stdout, "branches kept; bs prune deletes them once merged\n")
	}
	return nil
}

// noHarness is bs end's note on a worker the gate had nothing to walk for.
const noHarness = "no harness process ran for %s, so there was no background work to check"

// gateCode is bs's exit code for a refusal by the background-work gate, else 0.
func gateCode(err error) int {
	switch {
	case errors.Is(err, worker.ErrBackground):
		return exitBackground
	case errors.Is(err, worker.ErrCantCheck):
		return exitCantCheck
	}
	return 0
}

// Exit codes of bs prune when it keeps a branch.
const (
	exitKeptUnmerged = 3 // some branch has work not on the default branch, or is in use in a worktree
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
	p, lb, err := scope(ctx, "prune", *proj, "", nil, stderr)
	if err != nil {
		return err
	}
	defer lb.Close()
	cfg, err := config.Load(p.Name) // the delivery kind picks prune's proofs: manual never asks GitHub
	if err != nil {
		return err
	}
	pruned, err := newFleet(worker.Fleet{P: p, LB: lb, Delivery: cfg.Delivery}).Prune(ctx, *dry)
	if err != nil {
		return err
	}
	if len(pruned) == 0 {
		fmt.Fprintln(stdout, "nothing to prune: no ended worker has a branch left")
		return nil
	}
	code := 0
	for _, r := range pruned {
		switch {
		case r.Freed && *dry:
			fmt.Fprintf(stdout, "would remove the ended worker's clean worktree of %s, which nothing has open\n", r.Branch)
		case r.Freed:
			fmt.Fprintf(stdout, "removed the ended worker's clean worktree of %s, which nothing had open\n", r.Branch)
		}
		switch {
		case r.InUse != "":
			fmt.Fprintf(stdout, "kept %s: in use in worktree %s", r.Branch, r.InUse)
			if r.KeptWhy != "" {
				fmt.Fprintf(stdout, " (kept: %s)", r.KeptWhy)
			}
			if r.Prunable {
				fmt.Fprint(stdout, ", which is gone; git worktree prune frees it")
			}
			if r.Merge == worker.Unknown {
				fmt.Fprintf(stdout, "; could not check: %s\n", r.Why)
				code = exitKeptUnknown
			} else {
				fmt.Fprintln(stdout)
				code = max(code, exitKeptUnmerged)
			}
		case r.Merge == worker.Merged && *dry:
			fmt.Fprintf(stdout, "would delete local branch %s: merged, %s\n", r.Branch, r.Why)
		case r.Deleted:
			fmt.Fprintf(stdout, "deleted local branch %s: merged, %s\n", r.Branch, r.Why)
		case r.Merge == worker.Merged:
			fmt.Fprintf(stdout, "kept %s: merged, but deleting it failed: %v\n", r.Branch, r.DeleteErr)
			code = exitKeptUnknown
		case r.Merge == worker.NotMerged:
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

// expandAll is config.Expand of each of paths; nil stays nil, so no default is no --path.
func expandAll(paths []string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, config.Expand(p))
	}
	return out
}
