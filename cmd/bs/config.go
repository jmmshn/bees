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

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/guards"
	"github.com/jmmshn/bees/internal/project"
	"github.com/jmmshn/bees/internal/prompt"
	"github.com/jmmshn/bees/internal/worker"
)

// cmdConfig prints every setting as valid TOML, key = "value", with its source and any
// scope limit in a trailing comment, so a line can be copied into a config file.
func cmdConfig(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "init" {
		return configInit(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo; none: the global file only)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "usage: bs config [--project p] | bs config init [flags]")
		return errUsage
	}
	name := ""
	switch n, err := project.Name(*proj); {
	case err == nil:
		name = n
	case !errors.Is(err, project.ErrNoProject):
		return err
	}
	l, err := config.Load(name)
	if err != nil {
		return err
	}
	if name == "" {
		fmt.Fprintln(stdout, "# no project (run in its repo, or pass --project): global file only")
	} else {
		fmt.Fprintf(stdout, "# project %s\n", name)
	}
	for _, f := range l.Files {
		missing := ""
		if !f.Exists {
			missing = " (missing)"
		}
		fmt.Fprintf(stdout, "# %s file: %s%s\n", f.Scope, f.Path, missing)
	}
	if os.Getenv("BS_LOGBOOK_DIR") != "" {
		fmt.Fprintln(stdout, "# BS_LOGBOOK_DIR is set: it beats paths.logbook_dir in this shell, never in a herdr pane bs recorded, and bs queen and bs spawn refuse a logbook outside the global paths.logbook_dir")
	}
	if name != "" {
		p, err := locate(name, l, "")
		if err != nil {
			return err
		}
		from := "paths.logbook_dir, " + l.Sources["paths.logbook_dir"].String()
		if os.Getenv("BS_LOGBOOK") != "" {
			from = "env BS_LOGBOOK"
		}
		fmt.Fprintf(stdout, "# logbook: %s (%s)\n", p.Logbook, from)
		printRepo(stdout, p, l)
		printGuards(ctx, stdout, p, l)
	}
	if dir, err := prompt.Dir(); err == nil {
		fmt.Fprintf(stdout, "# cache (launch prompts, worker settings): %s\n", dir)
	} else {
		fmt.Fprintf(stdout, "# cache: unusable: %v\n", err)
	}
	for _, f := range l.Fields() {
		limit := ""
		if f.Scope != "both" {
			limit = "; " + f.Scope + " file only"
		}
		val := config.Quote(f.Value)
		if f.List {
			q := make([]string, len(f.Items))
			for i, it := range f.Items {
				q[i] = config.Quote(it)
			}
			val = "[" + strings.Join(q, ", ") + "]"
		}
		fmt.Fprintf(stdout, "%s = %s  # %s%s\n", f.Key, val, l.Sources[f.Key], limit)
	}
	return nil
}

// configInit writes a commented config file, the global one or with --project a project's,
// setting only the keys its flags name. It never overwrites a file.
func configInit(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("config init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "write projects/<project>.toml instead of the global config.toml")
	set := map[string]string{}
	for _, fl := range []struct{ name, key, usage string }{
		{"harness", "harness", "harness: claude_code | agy (global file only)"},
		{"workspace", "workspace.kind", "workspace.kind: git_worktree | none"},
		{"delivery", "delivery.kind", "delivery.kind: github_pr | manual"},
		{"fragment", "delivery.fragment", "delivery.fragment: a file of your own delivery wording"},
		{"repo", "paths.repo", "paths.repo: the project's directory, which workspace none shares (project file only)"},
	} {
		fs.Func(fl.name, fl.usage, func(v string) error { set[fl.key] = v; return nil })
	}
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "usage: bs config init [--project p] [--harness h] [--workspace k] [--delivery d] [--fragment file] [--repo dir]")
		return errUsage
	}
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	scope, path := "global", filepath.Join(dir, "config.toml")
	if *proj != "" {
		name, err := project.Name(*proj)
		if err != nil {
			return err
		}
		scope, path = "project", filepath.Join(dir, "projects", name+".toml")
	}
	text, err := config.Template(scope, set)
	if err != nil {
		fmt.Fprintf(stderr, "bs config init: %v\n", err)
		return errUsage
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s already exists: edit it, or move it aside to write a fresh one", path)
	}
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s; bs config shows what bs reads\n", path)
	return nil
}

// printRepo says which repo the repo chain found for p, from which link, and where
// workers' worktrees go.
func printRepo(stdout io.Writer, p project.Project, l *config.Loaded) {
	if err := p.RepoErr(); err != nil {
		fmt.Fprintf(stdout, "# repo: unusable (%s): %v\n", p.RepoFrom, err)
		return
	}
	if p.Repo == "" {
		fmt.Fprintf(stdout, "# repo: none: bs queen and bs spawn need one; set paths.repo in %s, or paths.repos\n", l.Files[len(l.Files)-1].Path)
		return
	}
	from := p.RepoFrom
	switch from {
	case "git":
		from = "the git repo you are in"
	case "paths.repo", "paths.repos":
		from += ", " + l.Sources[from].String()
	}
	fmt.Fprintf(stdout, "# repo: %s (%s)\n", p.Repo, from)
	if p.Base != "" {
		fmt.Fprintf(stdout, "# base: %s (a linked worktree: workers branch from its HEAD)\n", p.Base)
	}
	if l.Workspace.Kind == worker.NoWorkspace {
		if !p.RepoNamed() {
			fmt.Fprintf(stdout, "# workers: none start: workspace.kind = none opens them on paths.repo, which is unset (bs spawn refuses rather than share %s)\n", p.Repo)
			return
		}
		fmt.Fprintf(stdout, "# workers: share %s, each in its own herdr workspace, no worktree or branch (workspace.kind = none)\n", p.Repo)
		return
	}
	fmt.Fprintf(stdout, "# worker worktrees: %s/<worker>\n", p.Worktrees)
}

// printGuards prints the guards every worker of p gets: the deny globs, and the intents
// a harness's classifier may allow, with <worker> for a worker's branch.
func printGuards(ctx context.Context, stdout io.Writer, p project.Project, l *config.Loaded) {
	from := "guards.deny, " + l.Sources["guards.deny"].String()
	if len(l.Guards.Deny) == 0 {
		from = "the default for delivery.kind " + l.Delivery.Kind
	}
	if p.RepoErr() != nil || p.Repo == "" {
		fmt.Fprintln(stdout, "# guards: unknown until bs finds the repo, whose default branch they guard")
		return
	}
	r, err := workerGuards(ctx, p, l)
	if err != nil {
		fmt.Fprintf(stdout, "# guards: unusable, so bs queen and bs spawn refuse: %v\n", err)
		return
	}
	fmt.Fprintf(stdout, "# guards: a worker is denied (%s):\n", from)
	for _, d := range r.Deny {
		fmt.Fprintf(stdout, "#   %s\n", d)
	}
	fmt.Fprintln(stdout, "# and its harness may allow, judged by intent:")
	for _, a := range r.Allow {
		fmt.Fprintf(stdout, "#   %s\n", a)
	}
}

// workerGuards builds the guards every worker of p gets, with <worker> for its branch: the
// same check bs spawn makes, so bs config shows and bs queen refuses what every spawn would.
func workerGuards(ctx context.Context, p project.Project, l *config.Loaded) (guards.Rules, error) {
	w := guards.Worker{Branch: "<worker>", Worktree: filepath.Join(p.Worktrees, "<worker>"), Repo: p.Repo}
	if l.Workspace.Kind == worker.NoWorkspace {
		w = guards.Worker{Repo: p.Repo, Dir: p.Repo}
	}
	if len(l.Guards.AllowMergeInto) > 0 && l.Delivery.Kind != "manual" {
		w.Protected = protected(ctx, p.Repo) // a merge grant holds only on a protected default branch
	}
	return worker.Rules(ctx, p.Repo, l.Delivery, l.Guards, w)
}

// protected reports GitHub's word that repo's default branch is protected; no GitHub, or
// a gh failure, is no.
func protected(ctx context.Context, repo string) bool {
	g := newFleet(worker.Fleet{}).G
	if g == nil {
		return false
	}
	ok, err := g.Protected(ctx, repo)
	return err == nil && ok
}
