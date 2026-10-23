package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/core"
)

// configInit writes the session's global config file, or with --project a project's,
// holding every key it may set under a comment, commented out at its default; its flags
// set the step-2 answers of docs/SETUP-AGENT.md. It refuses a file Load would refuse, a
// workspace kind spawn won't make yet, and on a herdr --session toy a logbook dir that is the real
// one, and never overwrites a file.
func (r *run) configInit(fs *flag.FlagSet, args []string) error {
	fs.Init(bsname.Cmd+" config init", flag.ContinueOnError)
	project := fs.Bool("project", false, "write the project's file, projects/<project>.toml, instead of the global config.toml")
	set := map[string]string{}
	for _, f := range []struct{ name, key, usage string }{
		{"harness", "harness", "harness (global file): claude_code | agy"},
		{"logbook-dir", "paths.logbook_dir", "paths.logbook_dir (global file): where the logbooks live"},
		{"repos", "paths.repos", "paths.repos (global file): a directory of repos"},
		{"worktrees", "paths.worktrees", "paths.worktrees: where git_worktree workspaces go"},
		{"workspace", "workspace.kind", "workspace.kind: git_worktree (default: worked out; none waits for bees-13.10; a [workspace.command] table, set by hand, makes it command)"},
		{"repo", "paths.repo", "paths.repo (project file): the project's directory, when " + bsname.Cmd + " isn't run in it"},
	} {
		fs.Func(f.name, f.usage, func(v string) error { set[f.key] = v; return nil })
	}
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: %s config init [--project] [--harness H] [--logbook-dir D] [--repos D] [--worktrees D] [--workspace K] [--repo D]", errUsage, bsname.Cmd)
	}
	scope, path := "global", filepath.Join(r.s.Dir, "config.toml")
	if *project {
		if r.s.Project == "" {
			return &core.Exit{Code: core.ExitRefused, Msg: bsname.Cmd + " config init --project: no project: it isn't run in a git repo and BS_PROJECT is unset",
				Next: "run it in the project's repo, or with BS_PROJECT=<project> outside any repo"}
		}
		scope, path = "project", filepath.Join(r.s.Dir, "projects", r.s.Project+".toml")
	}
	text, err := config.Template(scope, set)
	if err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if err := r.initRefusals(scope, set, text); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Written aside, then linked into place, which fails on an existing file: a failed
	// write leaves no half file that every retry would refuse.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-init-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), path); errors.Is(err, os.ErrExist) {
		return &core.Exit{Code: core.ExitRefused, Msg: path + " already exists; nothing was written", Next: "edit it, or move it aside to write a fresh one"}
	} else if err != nil {
		return err
	}
	fmt.Fprintf(r.out, "wrote %s; %s config shows what it reads\n", path, bsname.Cmd)
	return nil
}

// initRefusals refuses what bs would refuse of text, the file config init is about to
// write as scope: a value Load refuses (read over the global file, for a project's), a
// workspace kind spawn won't make (unmade), a paths.repo that isn't a directory (bs never makes one), and on a herdr --session
// toy a logbook dir that is the real logbooks'.
func (r *run) initRefusals(scope string, set map[string]string, text string) error {
	dir, err := os.MkdirTemp("", "bs-config-init-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	in := config.Input{Dir: dir, Home: core.Getenv(r.env.Vars, "HOME")}
	file := filepath.Join(dir, "config.toml")
	if scope == "project" {
		in.Project, file = r.s.Project, filepath.Join(dir, "projects", r.s.Project+".toml")
		if err := os.Mkdir(filepath.Dir(file), 0o700); err != nil {
			return err
		}
		switch b, err := os.ReadFile(filepath.Join(r.s.Dir, "config.toml")); {
		case err == nil:
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), b, 0o600); err != nil {
				return err
			}
		case !errors.Is(err, os.ErrNotExist): // unreadable is not absent (Rule G)
			return err
		}
	}
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		return err
	}
	l, err := config.Load(in)
	if err != nil {
		return &core.Exit{Code: core.ExitRefused, Msg: bsname.Cmd + " config init: nothing was written", Err: errors.New(strings.ReplaceAll(err.Error(), dir, r.s.Dir))}
	}
	if set["workspace.kind"] == config.CommandKind {
		return &core.Exit{Code: core.ExitRefused, Msg: bsname.Cmd + " config init: --workspace command: a command workspace is its [workspace.command] table (create, remove, clean, dir), which config init doesn't write; nothing was written",
			Next: "leave --workspace off, then write the table into the file it makes (docs/SETUP.md has examples)"}
	}
	if k := set["workspace.kind"]; k != "" && unmade(k) != "" {
		return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("%s config init: --workspace %s: %s; nothing was written", bsname.Cmd, k, unmade(k)),
			Next: "leave --workspace off for the worked-out kind, or pass git_worktree"}
	}
	if repo := set["paths.repo"]; repo != "" {
		switch fi, err := os.Stat(l.Expand(repo)); {
		case errors.Is(err, os.ErrNotExist) || err == nil && !fi.IsDir():
			return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("%s config init: --repo %s: no such directory; nothing was written", bsname.Cmd, repo), Next: "make it first: " + bsname.Cmd + " never does"}
		case err != nil:
			return &core.Exit{Code: core.ExitCantCheck, Msg: bsname.Cmd + " config init: can't look at --repo " + repo + "; nothing was written", Err: err}
		}
	}
	if scope == "global" && r.s.Session != config.DefaultSession {
		lb := l.Expand(l.Paths.LogbookDir)
		for _, real := range realLogbookDirs(r.env.Vars) {
			if samePath(lb, real) {
				return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("%s config init: paths.logbook_dir would be the real logbooks' dir, %s, which a herdr --session toy never uses; nothing was written", bsname.Cmd, lb),
					Next: bsname.Cmd + " config init --logbook-dir <a dir of this session's own>"}
			}
		}
	}
	return nil
}
