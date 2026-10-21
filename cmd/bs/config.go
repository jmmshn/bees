package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/project"
)

// cmdConfig prints every setting as valid TOML, key = "value", with its source and any
// scope limit in a trailing comment, so a line can be copied into a config file.
func cmdConfig(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo; none: the global file only)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "usage: bs config [--project p]")
		return errUsage
	}
	name := ""
	switch p, err := project.Resolve(*proj); {
	case err == nil:
		name = p.Name
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
	fmt.Fprintln(stdout, "# the queen and every worker run on claude_code: bs's only harness, so no key selects one yet")
	for _, f := range l.Fields() {
		limit := ""
		if f.Scope != "both" {
			limit = "; " + f.Scope + " file only"
		}
		fmt.Fprintf(stdout, "%s = %s  # %s%s\n", f.Key, tomlString(f.Value), l.Sources[f.Key], limit)
	}
	return nil
}

// tomlString quotes s as a TOML basic string; Go's %q writes escapes TOML lacks (\a, \v).
// ponytail: invalid UTF-8 prints as U+FFFD, since TOML strings can't hold it.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range strings.ToValidUTF8(s, "\uFFFD") {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
