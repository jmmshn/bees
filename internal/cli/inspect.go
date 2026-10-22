package cli

import (
	"flag"
	"fmt"

	"github.com/jmmshn/bees/internal/bsname"
)

// config prints every setting as TOML, key = value, with its source and any scope limit
// in a trailing comment, after a header of what bs worked out around it: so a line can be
// copied into the file the header names.
func (r *run) config(fs *flag.FlagSet, args []string) error {
	if len(args) > 0 && args[0] == "init" {
		return r.configInit(fs, args[1:])
	}
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
		fmt.Fprintln(w, "# project: none (run in its repo, or set BS_PROJECT outside a herdr pane): global file only")
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
