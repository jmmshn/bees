package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/port"
)

// bs epic and bs bead keep the queen and workers inside their project's beads (R32): the
// bead store is shared by every project, and bd list and bd create see and write them all.

var beadUsage = strings.ReplaceAll(`{cmd} bead ls [--all] [<bd query expression>]
       {cmd} bead search [--all] <text>
       {cmd} bead new --parent <project>-N [--description <text>] [--type <type>] [--priority <0-4>] <title>`, "{cmd}", bsname.Cmd)

// beadNumber is what follows the project in one of its bead ids: -N[.M…].
var beadNumber = regexp.MustCompile(`^-\d+(\.\d+)*$`)

// interleaved parses fs's flags wherever they sit among args, as the old bs's bead and
// epic did: a title typed before --description would otherwise swallow it as words. Every
// argument after a "--" is positional. It returns the positional arguments in order.
func interleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, tail = args[:i], args[i+1:]
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if args = fs.Args(); len(args) == 0 {
			return append(pos, tail...), nil
		}
		pos, args = append(pos, args[0]), args[1:]
	}
}

// project is the caller's project, refused when bs can't tell it.
func (r *run) project() (string, error) {
	return r.s.Project, r.s.needProject()
}

// bd runs bd args, its stderr folded into the error.
func (r *run) bd(args ...string) ([]byte, error) {
	out, err := r.ports.Run.Run(r.ctx, "", append([]string{"bd"}, args...)...)
	var ee *port.ExitError
	if errors.As(err, &ee) {
		return nil, fmt.Errorf("bd %s: exit %d: %s", args[0], ee.Code, strings.TrimSpace(string(ee.Stderr)+string(ee.Stdout)))
	}
	if err != nil {
		return nil, fmt.Errorf("bd %s: %w", args[0], err)
	}
	return out, nil
}

// epic creates the project's next epic, <project>-<n>, n one past the highest number any
// of its beads uses, open or closed.
func (r *run) epic(fs *flag.FlagSet, args []string) error {
	title, err := interleaved(fs, args)
	if err != nil {
		return err
	}
	if len(title) == 0 {
		return fmt.Errorf("%w: %s epic <title>", errUsage, bsname.Cmd)
	}
	p, err := r.project()
	if err != nil {
		return err
	}
	out, err := r.bd("list", "--all", "--limit", "0", "--json")
	if err != nil {
		return err
	}
	var beads []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &beads); err != nil {
		return fmt.Errorf("bd list: %w", err)
	}
	top := 0
	for _, b := range beads {
		rest, ok := strings.CutPrefix(b.ID, p+"-")
		rest, _, _ = strings.Cut(rest, ".") // a child proves its epic's number is taken
		if n, err := strconv.Atoi(rest); ok && err == nil && n > top {
			top = n
		}
	}
	id := fmt.Sprintf("%s-%d", p, top+1)
	// --force: a project's ids don't use the store's own prefix.
	if _, err := r.bd("create", "--silent", "--force", "--type", "epic", "--id", id, "--title", strings.Join(title, " ")); err != nil {
		return err
	}
	fmt.Fprintln(r.out, id)
	fmt.Fprintf(r.err, "add beads with: %s bead new --parent %s \"...\"\n", bsname.Cmd, id)
	return nil
}

// bead lists the project's beads (ls) or files one under one of them (new).
func (r *run) bead(fs *flag.FlagSet, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: %s", errUsage, beadUsage)
	}
	p, err := r.project()
	if err != nil {
		return err
	}
	switch args[0] {
	case "ls":
		all := fs.Bool("all", false, "include closed beads")
		query, err := interleaved(fs, args[1:])
		if err != nil {
			return err
		}
		// Project names are only letters and digits, so the wildcard can't match another
		// project's beads; a query whose parentheses don't balance could step out of the
		// AND, so it is refused. A scope, not a guard: bd query itself reads every project.
		if !balanced(query) {
			return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("bead ls: %q: its parentheses don't balance, so it could reach outside %s", strings.Join(query, " "), p)}
		}
		expr := fmt.Sprintf("id=%q", p+"-*")
		if !*all {
			expr += " AND status!=closed"
		}
		if len(query) > 0 {
			expr += " AND (" + strings.Join(query, " ") + ")"
		}
		out, err := r.bd("query", "--limit", "0", expr)
		if err != nil {
			return err
		}
		_, err = r.out.Write(out)
		return err
	case "search":
		all := fs.Bool("all", false, "include closed beads")
		words, err := interleaved(fs, args[1:])
		if err != nil {
			return err
		}
		if len(words) == 0 {
			return fmt.Errorf("%w: %s", errUsage, beadUsage)
		}
		return r.search(p, strings.Join(words, " "), *all)
	case "new":
		parent := fs.String("parent", "", "the project bead to file under, <project>-N[.M…] (required)")
		desc := fs.String("description", "", "description")
		typ := fs.String("type", "", "bd issue type (default: bd's, task)")
		prio := fs.String("priority", "", "priority 0-4 (default: bd's, 2)")
		title, err := interleaved(fs, args[1:])
		if err != nil {
			return err
		}
		if len(title) == 0 {
			return fmt.Errorf("%w: %s", errUsage, beadUsage)
		}
		if *parent == "" {
			return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("needs --parent %s-N: a project's top-level beads are epics", p),
				Next: bsname.Cmd + ` epic "<title>"`}
		}
		if rest, ok := strings.CutPrefix(*parent, p); !ok || !beadNumber.MatchString(rest) {
			return &core.Exit{Code: core.ExitRefused, Msg: fmt.Sprintf("%s is not a %s bead (want %s-N[.M…]); nothing was filed", *parent, p, p),
				Next: bsname.Cmd + " bead ls"}
		}
		bdArgs := []string{"create", "--silent", "--force", "--parent", *parent, "--title", strings.Join(title, " ")}
		for _, f := range [][2]string{{"--description", *desc}, {"--type", *typ}, {"--priority", *prio}} {
			if f[1] != "" {
				bdArgs = append(bdArgs, f[0], f[1])
			}
		}
		out, err := r.bd(bdArgs...)
		if err != nil {
			return err
		}
		fmt.Fprintln(r.out, strings.TrimSpace(string(out)))
		return nil
	}
	return fmt.Errorf("%w: %s", errUsage, beadUsage)
}

// search prints each of project p's beads whose title, description, notes or design holds
// text, ignoring case, with a snippet of each field that does. bd query has no design field
// and bd search reads titles only, so bs reads the beads whole (bd show) and matches itself.
func (r *run) search(p, text string, all bool) error {
	expr := fmt.Sprintf("id=%q", p+"-*")
	if !all {
		expr += " AND status!=closed"
	}
	out, err := r.bd("query", "--json", "--limit", "0", expr)
	if err != nil {
		return err
	}
	var listed []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return fmt.Errorf("bd query: %w", err)
	}
	var beads []struct {
		ID, Status, Title, Description, Notes, Design string
	}
	if len(listed) > 0 {
		ids := []string{"show", "--json"}
		for _, b := range listed {
			ids = append(ids, b.ID)
		}
		if out, err = r.bd(ids...); err != nil {
			return err
		}
		if err := json.Unmarshal(out, &beads); err != nil {
			return fmt.Errorf("bd show: %w", err)
		}
	}
	scope := "open " + p + " bead"
	if all {
		scope = p + " bead"
	}
	found := 0
	for _, b := range beads {
		var hits []string
		for _, f := range [][2]string{{"title", b.Title}, {"description", b.Description}, {"notes", b.Notes}, {"design", b.Design}} {
			if s, ok := snippet(f[1], text); ok {
				hits = append(hits, fmt.Sprintf("    %s: %s", f[0], s))
			}
		}
		if len(hits) == 0 {
			continue
		}
		found++
		fmt.Fprintf(r.out, "%s [%s] %s\n%s\n", b.ID, b.Status, b.Title, strings.Join(hits, "\n"))
	}
	if found == 0 {
		msg := fmt.Sprintf("no %s mentions %q", scope, text)
		if !all {
			msg += "; --all searches closed ones too"
		}
		fmt.Fprintln(r.err, msg)
	}
	return nil
}

// snippet finds text in s, ignoring case, and returns the line around its first match,
// whitespace collapsed and trimmed to about 60 characters either side.
func snippet(s, text string) (string, bool) {
	i := strings.Index(strings.ToLower(s), strings.ToLower(text))
	if i < 0 {
		return "", false
	}
	const pad = 60
	lo, hi := max(i-pad, 0), min(i+len(text)+pad, len(s))
	for lo > 0 && !utf8.RuneStart(s[lo]) {
		lo--
	}
	for hi < len(s) && !utf8.RuneStart(s[hi]) {
		hi++
	}
	out := strings.Join(strings.Fields(s[lo:hi]), " ")
	if lo > 0 {
		out = "…" + out
	}
	if hi < len(s) {
		out += "…"
	}
	return out, true
}

// balanced reports whether the parentheses in args, outside quotes, never close more than
// they opened and all close.
func balanced(args []string) bool {
	depth, quote := 0, rune(0)
	for _, c := range strings.Join(args, " ") {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth--; depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}
