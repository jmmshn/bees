package cli

import (
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/jmmshn/bees/cmd/bsx/internal/bsname"
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
		return fmt.Errorf("%w: "+bsname.Cmd+" config takes no arguments", errUsage)
	}
	s, w := r.s, r.out
	missing := ""
	if s.Missing {
		missing = " (missing: every command but config and doctor refuses)"
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
		fmt.Fprintln(w, "# project: none (run in its repo, or set BS_PROJECT outside a recorded herdr pane): global file only")
	} else {
		fmt.Fprintf(w, "# project: %s (%s)\n# logbook: %s (paths.logbook_dir, %s)\n", s.Project, s.ProjectFrom, s.Logbook, s.Sources["paths.logbook_dir"])
	}
	if s.Repo == "" {
		fmt.Fprintln(w, "# repo: unknown (run in it, or set paths.repo)")
	} else {
		fmt.Fprintf(w, "# repo: %s (%s)\n# worktrees: %s\n", s.Repo, s.RepoFrom, s.Worktrees)
	}
	kind, why := s.Kind(s.GitTop)
	fmt.Fprintf(w, "# workspace kind: %s (%s)\n", kind, why)
	fmt.Fprintf(w, "# cache: %s\n", r.ports.CacheDir)
	for _, f := range s.Fields() {
		note := ""
		if f.Scope != "both" {
			note = "; " + f.Scope + " file only"
		}
		if f.Unread != "" {
			note += "; " + f.Unread
		}
		fmt.Fprintf(w, "%s = %s  # %s%s\n", f.Key, f.Value, f.Source, note)
	}
	return nil
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
		case syscall.Access(dir, wOK) == nil:
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
	if c := s.Workspace.Command; c != nil && len(c.Missing()) > 0 {
		line("FAIL", "workspace.command", "%s unset: spawn refuses a command workspace without them", strings.Join(c.Missing(), ", "))
	}
	// A key a file sets that bsx doesn't act on yet: guards.deny set and ignored is a
	// guard the keeper thinks is on.
	unread := map[string][]string{}
	for _, f := range s.Fields() {
		if f.Unread != "" && f.Source.Kind != "default" {
			unread[f.Unread] = append(unread[f.Unread], f.Key)
		}
	}
	for _, why := range slices.Sorted(maps.Keys(unread)) {
		line("WARN", "config", "%s set, but %s: bsx ignores it", strings.Join(unread[why], ", "), why)
	}
	for _, a := range s.argvs() {
		if _, ok := lookPath(path, a[1]); !ok {
			line("FAIL", a[0], "%s is not on PATH", a[1])
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

// wOK is access(2)'s W_OK: the dir is writable.
const wOK = 2

// argvs is every command the config names, as {key, argv[0]}, in key order.
func (s *settings) argvs() [][2]string {
	var out [][2]string
	add := func(key string, argv []string) {
		if len(argv) > 0 && argv[0] != "" {
			out = append(out, [2]string{key, argv[0]})
		}
	}
	if c := s.Workspace.Command; c != nil {
		add("workspace.command.create", c.Create)
		add("workspace.command.remove", c.Remove)
		add("workspace.command.clean", c.Clean)
	}
	for _, name := range slices.Sorted(maps.Keys(s.Checks)) {
		add("checks."+name+".run", s.Checks[name].Run)
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
