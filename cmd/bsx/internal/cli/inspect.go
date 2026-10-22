package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jmmshn/bees/cmd/bsx/internal/config"
	"github.com/jmmshn/bees/cmd/bsx/internal/core"
	"github.com/jmmshn/bees/cmd/bsx/internal/herdr"
)

// config prints every setting as TOML, key = value, with its source and any scope limit
// in a trailing comment, after a header of what bs worked out around it: so a line can be
// copied into the file the header names.
func (r *run) config(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: bsx config takes no arguments", errUsage)
	}
	s, w := r.s, r.out
	missing := ""
	if s.Missing {
		missing = " (missing: writing commands refuse)"
	}
	fmt.Fprintf(w, "# config dir: %s, herdr session %s%s\n", s.Dir, s.Session, missing)
	for _, f := range s.Files {
		state := ""
		if !f.Exists {
			state = " (missing)"
		}
		fmt.Fprintf(w, "# %s file: %s%s\n", f.Scope, f.Path, state)
	}
	if s.Project == "" {
		fmt.Fprintln(w, "# project: none (run in its repo, or set BS_PROJECT outside a herdr pane): global file only")
	} else {
		fmt.Fprintf(w, "# project: %s (%s)\n# logbook: %s (paths.logbook_dir, %s)\n", s.Project, s.ProjectFrom, s.Logbook, s.Sources["paths.logbook_dir"])
	}
	if s.Repo == "" {
		fmt.Fprintln(w, "# repo: unknown (run in it, or set paths.repo)")
	} else {
		fmt.Fprintf(w, "# repo: %s (%s)\n# worktrees: %s\n", s.Repo, s.RepoFrom, s.Worktrees)
	}
	fmt.Fprintf(w, "# workspace kind: %s (%s)\n", s.Kind(s.GitTop), s.kindWhy())
	fmt.Fprintf(w, "# cache: %s\n", r.ports.CacheDir)
	for _, f := range s.Fields() {
		limit := ""
		if f.Scope != "both" {
			limit = "; " + f.Scope + " file only"
		}
		fmt.Fprintf(w, "%s = %s  # %s%s\n", f.Key, f.Value, f.Source, limit)
	}
	return nil
}

// kindWhy says how bs worked out the workspace kind.
func (s *settings) kindWhy() string {
	switch {
	case s.Workspace.Kind != "":
		return "workspace.kind, " + s.Sources["workspace.kind"].String()
	case s.Workspace.Command != nil:
		return "[workspace.command] is set, " + s.Sources["workspace.command"].String()
	case s.GitTop:
		return "worked out: the repo is a git checkout's top level"
	}
	return "worked out: no repo that is a git checkout's top level"
}

// doctor checks what bsx needs, one line each: ok, WARN or FAIL, a FAIL saying what to do.
// Any FAIL exits 1. More checks come with the setup bead (ADR-0010 Config).
func (r *run) doctor(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, failed := r.s, 0
	line := func(level, name, format string, a ...any) {
		if level == "FAIL" {
			failed++
		}
		fmt.Fprintf(r.out, "%-4s %s: %s\n", level, name, fmt.Sprintf(format, a...))
	}
	if s.Missing {
		line("FAIL", "config dir", "%s is missing for herdr session %s: mkdir -p it and write its config.toml", s.Dir, s.Session)
	} else {
		line("ok", "config dir", "%s (herdr session %s)", s.Dir, s.Session)
	}
	switch {
	case s.Project == "":
		line("WARN", "project", "none: run in its repo")
	case s.Repo == "":
		line("WARN", "repo", "project %s has no repo: run in it, or set paths.repo", s.Project)
	default:
		line("ok", "project", "%s, repo %s", s.Project, s.Repo)
	}
	if s.Workspace.Kind == config.NoWorkspace && s.GitTop {
		line("WARN", "workspace", "workspace.kind = none over a git checkout: workers share it with no branch (bees-12.28)")
	}
	if s.Project != "" {
		dir := filepath.Dir(s.Logbook)
		switch err := s.checkLogbook(r.env); {
		case err != nil:
			line("FAIL", "logbook dir", "%v", err)
		case syscall.Access(dir, 2) == nil:
			line("ok", "logbook dir", "%s is writable", dir)
		default:
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				line("ok", "logbook dir", "%s: made at the first write", dir)
			} else {
				line("FAIL", "logbook dir", "%s isn't writable", dir)
			}
		}
	}
	path := core.Getenv(r.env.Vars, "PATH")
	for _, p := range []struct{ name, program string }{{"harness " + s.Harness, harnessProgram(s.Harness)}, {"herdr", "herdr"}} {
		if at, ok := lookPath(path, p.program); ok {
			line("ok", p.name, "%s", at)
		} else {
			line("FAIL", p.name, "%s is not on PATH", p.program)
		}
	}
	if _, err := (herdr.Client{Run: r.ports.Run, Session: s.Session}).Panes(r.ctx, ""); err != nil {
		line("FAIL", "herdr server", "session %s doesn't answer: %v", s.Session, err)
	} else {
		line("ok", "herdr server", "session %s answers", s.Session)
	}
	for _, a := range s.argvs() {
		if _, ok := lookPath(path, a.program); !ok {
			line("FAIL", a.key, "%s is not on PATH", a.program)
		}
	}
	for _, kv := range r.env.Vars {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "BS_") {
			line("WARN", "env", "%s is set: bs sets no BS_* in a session's env, and ignores it inside a herdr pane", k)
		}
	}
	if failed > 0 {
		return &core.Exit{Code: core.ExitFail, Msg: fmt.Sprintf("%d check(s) failed", failed)}
	}
	return nil
}

// argvs is argv[0] of every command the config names, by its key.
func (s *settings) argvs() []struct{ key, program string } {
	var out []struct{ key, program string }
	if c := s.Workspace.Command; c != nil {
		for key, argv := range map[string][]string{"create": c.Create, "remove": c.Remove, "clean": c.Clean} {
			out = append(out, struct{ key, program string }{"workspace.command." + key, argv[0]})
		}
	}
	for name, c := range s.Checks {
		out = append(out, struct{ key, program string }{"checks." + name + ".run", c.Run[0]})
	}
	return out
}

// lookPath finds program on path, the caller's PATH rather than bs's own.
func lookPath(path, program string) (string, bool) {
	if strings.Contains(program, "/") {
		fi, err := os.Stat(program)
		return program, err == nil && fi.Mode()&0o111 != 0
	}
	for _, d := range filepath.SplitList(path) {
		at := filepath.Join(d, program)
		if fi, err := os.Stat(at); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return at, true
		}
	}
	return "", false
}
