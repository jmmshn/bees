package cli

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/proc"
	"github.com/jmmshn/bees/internal/review/github"
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

// configInit writes the session's global config file, or with --project a project's,
// holding every key it may set under a comment, commented out at its default; its flags
// set the step-2 answers of docs/SETUP-AGENT.md. It refuses a file Load would refuse, on a
// herdr --session toy a logbook dir that is the real one, and never overwrites a file.
func (r *run) configInit(fs *flag.FlagSet, args []string) error {
	fs.Init("bs config init", flag.ContinueOnError)
	project := fs.Bool("project", false, "write the project's file, projects/<project>.toml, instead of the global config.toml")
	set := map[string]string{}
	for _, f := range []struct{ name, key, usage string }{
		{"harness", "harness", "harness (global file): claude_code"},
		{"logbook-dir", "paths.logbook_dir", "paths.logbook_dir (global file): where the logbooks live"},
		{"repos", "paths.repos", "paths.repos (global file): a directory of repos"},
		{"worktrees", "paths.worktrees", "paths.worktrees: where git_worktree workspaces go"},
		{"workspace", "workspace.kind", "workspace.kind: git_worktree | none | command (default: worked out)"},
		{"repo", "paths.repo", "paths.repo (project file): the project's directory, when " + bsname.Cmd + " isn't run in it"},
	} {
		fs.Func(f.name, f.usage, func(v string) error { set[f.key] = v; return nil })
	}
	if err := fs.Parse(args); err != nil {
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
// paths.repo that isn't a directory (bs never makes one), and on a herdr --session toy a
// logbook dir that is the real logbooks'.
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

// doctor checks what bs needs, one line each: ok, WARN (bs works, with a caveat) or
// FAIL (a bs command would refuse or misbehave), a FAIL or WARN saying what to do. Any
// FAIL exits 1. r.s.Broken, the error that stopped bs reading the config, is a FAIL of its
// own, and the checks that need no config still run on the defaults.
func (r *run) doctor(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: %s doctor takes no arguments", errUsage, bsname.Cmd)
	}
	d := &doctor{r: r, path: core.Getenv(r.env.Vars, "PATH")}
	s := r.s
	switch {
	case s.Broken != nil && s.GlobalBroken:
		d.line("FAIL", "config", "%s; the checks below run on the defaults", strings.ReplaceAll(s.Broken.Error(), "\n", "\n     "))
	case s.Broken != nil:
		d.line("FAIL", "project", "%s; the checks below run on the global file alone", strings.ReplaceAll(s.Broken.Error(), "\n", "\n     "))
	case s.Missing:
		d.line("FAIL", "config dir", "%s is missing for herdr session %s: %s config init writes it", s.Dir, s.Session, bsname.Cmd)
	default:
		var files []string
		for _, f := range s.Files {
			state := ""
			if !f.Exists {
				state = " (missing: defaults)"
			}
			files = append(files, f.Scope+" "+f.Path+state)
		}
		d.line("ok", "config", "herdr session %s: %s", s.Session, strings.Join(files, ", "))
	}
	if s.Broken == nil {
		d.project()
	} else {
		d.line("skip", "project", "project, workspace and logbook dir: bs can't work out the project (above)")
	}
	d.harness()
	d.herdr()
	d.bd()
	if s.Broken == nil {
		d.review()
		d.commands()
		d.unread()
	} else {
		d.line("skip", "project", "gh and the GitHub remote, the config's argv, keys bs doesn't read yet: they need the project (above)")
	}
	stray := 0
	for _, kv := range r.env.Vars {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "BS_") {
			stray++
		}
		switch {
		case !strings.HasPrefix(k, "BS_"):
		case !slices.Contains(envKeys, k):
			d.line("WARN", "env", "%s is set: %s doesn't read it (the shared bs may); it does nothing here", k, bsname.Cmd)
		case s.Pane != nil:
			d.line("WARN", "env", "%s is set in %s's pane, where %s ignores it: %s sets no BS_* in a session's env, so it leaked from the shell that started herdr", k, s.Pane.Who.Name, bsname.Cmd, bsname.Cmd)
		default:
			d.line("WARN", "env", "%s is set in this shell and applies to every %s command run from it: unset it here unless you meant it", k, bsname.Cmd)
		}
	}
	if stray == 0 {
		d.line("ok", "env", "no BS_* variables set")
	}
	if d.failed > 0 {
		return &core.Exit{Code: core.ExitFail, Msg: fmt.Sprintf("%d check(s) failed", d.failed)}
	}
	return nil
}

// doctor is one bs doctor run: the caller's PATH, and how many checks failed.
type doctor struct {
	r      *run
	path   string
	failed int
}

func (d *doctor) line(level, name, format string, a ...any) {
	if level == "FAIL" {
		d.failed++
	}
	fmt.Fprintf(d.r.out, "%-4s %s: %s\n", level, name, fmt.Sprintf(format, a...))
}

// minVersion is the oldest release of each tool bs works with, by program.
var minVersion = map[string]string{
	"claude": "2.1.296", // cross-session messaging, which the doorbell rides on
	"bd":     "1.1.0",   // bd query
}

// version runs program --version, found on the caller's PATH, and returns the first x.y.z
// it prints and where the program is, refusing one older than minVersion.
func (d *doctor) version(program string) (at, v string, err error) {
	at, ok := lookPath(d.path, program)
	if !ok {
		return "", "", fmt.Errorf("%s is not on PATH: install it (docs/SETUP-AGENT.md, step 3)", program)
	}
	ctx, cancel := context.WithTimeout(d.r.ctx, 5*time.Second)
	defer cancel()
	out, err := d.r.ports.Run.Run(ctx, "", at, "--version")
	if err != nil {
		return at, "", fmt.Errorf("%s --version: %w", at, err)
	}
	if v = semver.FindString(string(out)); v == "" {
		return at, "", fmt.Errorf("%s --version printed no version: %s", at, strings.TrimSpace(string(out)))
	}
	if min, ok := minVersion[program]; ok && older(v, min) {
		return at, v, fmt.Errorf("%s %s is older than %s, the oldest bees works with: upgrade it (docs/SETUP-AGENT.md, step 3)", at, v, min)
	}
	return at, v, nil
}

// semver is the first x.y.z in a --version line.
var semver = regexp.MustCompile(`\d+\.\d+\.\d+`)

// older reports whether version a (x.y.z) is before b. Both come from semver, so each
// part is digits and Atoi can't fail.
func older(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range 3 {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// project checks the project, its repo and workspace kind, and its logbook dir.
func (d *doctor) project() {
	s := d.r.s
	switch {
	case s.Project == "":
		d.line("WARN", "project", "none: run in its repo (or with BS_PROJECT=<project> outside any repo); project checks skipped")
		return
	case s.Repo == "":
		d.line("WARN", "repo", "project %s (%s) has no repo: run in it, or set paths.repo in its project file", s.Project, s.ProjectFrom)
	default:
		kind, why := s.Kind(s.GitTop)
		d.line("ok", "project", "%s (%s), repo %s, workspace %s (%s)", s.Project, s.ProjectFrom, s.Repo, kind, why)
	}
	// Until bees-13.10 and 13.11, spawn makes only git_worktree workspaces: any other kind
	// is a spawn refused, said here first.
	switch kind, why := s.Kind(s.GitTop); {
	case kind == config.GitWorktree:
	case s.Repo == "":
	case kind == config.NoWorkspace && s.GitTop:
		d.line("FAIL", "workspace", "workspace.kind = none over a git checkout (%s): %s spawn refuses it, and workers would share %s with no branch of their own (bees-12.28); drop the override for git_worktree", why, bsname.Cmd, s.Repo)
	case kind == config.NoWorkspace:
		d.line("FAIL", "workspace", "none (%s): %s spawn makes only git_worktree workspaces until bees-13.10; make the project a git checkout, or wait for none", why, bsname.Cmd)
	default:
		d.line("FAIL", "workspace", "%s (%s): %s spawn makes only git_worktree workspaces until bees-13.11; drop [workspace.command] and workspace.kind", kind, why, bsname.Cmd)
	}
	dir := filepath.Dir(s.Logbook)
	switch err := s.checkLogbook(d.r.env); {
	case err != nil:
		d.line("FAIL", "logbook dir", "%v; set paths.logbook_dir in %s", err, filepath.Join(s.Dir, "config.toml"))
	case syscall.Access(dir, wOK) == nil:
		d.line("ok", "logbook dir", "%s is writable", dir)
	default:
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			d.line("ok", "logbook dir", "%s: made at the first write", dir)
		} else {
			d.line("FAIL", "logbook dir", "%s isn't writable", dir)
		}
	}
}

// harness checks the configured harness's program and version, and the modes of its ring
// sockets (ADR-0009 T3) when it can tell them.
func (d *doctor) harness() {
	s := d.r.s
	name := "harness " + s.Harness
	at, v, err := d.version(harnessProgram(s.Harness))
	if err != nil {
		d.line("FAIL", name, "%v", err)
	} else {
		d.line("ok", name, "%s %s", at, v)
	}
	h := d.r.ports.Harness
	if h == nil && d.r.ports.NewHarness != nil {
		h = d.r.ports.NewHarness(s.Harness, "")
	}
	m, ok := h.(interface {
		Modes(home string) (string, error)
	})
	if !ok {
		return
	}
	if what, err := m.Modes(core.Getenv(d.r.env.Vars, "HOME")); err != nil {
		d.line("FAIL", "ring sockets", "%v: another user could ring a session; chmod it (0700 a dir, 0600 a file)", err)
	} else {
		d.line("ok", "ring sockets", "%s", what)
	}
}

// herdr checks the session's herdr server: running, its endpoint compatible with the
// client, not older than the client (bs's herdr probes ran on the client's version,
// bees-12.44), and its socket closed to everyone else (T3).
func (d *doctor) herdr() {
	s := d.r.s
	name := "herdr session " + s.Session
	if at, ok := lookPath(d.path, "herdr"); !ok {
		d.line("FAIL", "herdr", "herdr is not on PATH: install it (docs/SETUP-AGENT.md, step 3)")
		return
	} else {
		d.line("ok", "herdr", "%s", at)
	}
	ctx, cancel := context.WithTimeout(d.r.ctx, 5*time.Second)
	defer cancel()
	argv := []string{"herdr", "status"}
	if s.Session != config.DefaultSession {
		argv = []string{"herdr", "--session", s.Session, "status"}
	}
	out, err := d.r.ports.Run.Run(ctx, "", argv...)
	if err != nil {
		d.line("FAIL", name, "%s: %v", strings.Join(argv, " "), err)
		return
	}
	st := herdrStatus(string(out))
	switch {
	case st["server.status"] != "running":
		self, _ := os.Executable()
		d.line("FAIL", name, "server %s (socket %s): start it with this build first on its PATH, PATH=%s:$PATH herdr --session %s, then run %s in one of its panes",
			cmp.Or(st["server.status"], "status unknown"), st["server.socket"], filepath.Dir(self), s.Session, bsname.Cmd)
		return
	case st["server.endpoint_compatible"] == "no" || st["server.private_protocol_compatible"] == "no":
		d.line("FAIL", name, "client %s can't talk to server %s: restart the server (herdr --session %s server stop, then start it again)", st["client.version"], st["server.version"], s.Session)
	case semver.MatchString(st["server.version"]) && semver.MatchString(st["client.version"]) && older(st["server.version"], st["client.version"]):
		d.line("WARN", name, "server %s is older than client %s, still running the binary it started from: restart it (herdr --session %s server stop, then start it again) when no session you need runs; bees' herdr probes ran on the client's version", st["server.version"], st["client.version"], s.Session)
	case st["update.restart_needed"] == "yes":
		d.line("WARN", name, "herdr says its server needs a restart (update.restart_needed): herdr --session %s server stop, then start it again, when no session you need runs", s.Session)
	default:
		d.line("ok", name, "server %s running, client %s", st["server.version"], st["client.version"])
	}
	switch sock := st["server.socket"]; sock {
	case "":
		d.line("FAIL", "herdr socket", "herdr status named no socket")
	default:
		if err := proc.Private(sock); err != nil {
			d.line("FAIL", "herdr socket", "%v: a socket open to others lets them drive this herdr; chmod 600 it, or restart the server", err)
		} else {
			d.line("ok", "herdr socket", "%s is the user's alone", sock)
		}
	}
}

// herdrStatus is herdr status's text as section.key → value.
func herdrStatus(out string) map[string]string {
	m, section := map[string]string{}, ""
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), ":")
		switch {
		case !ok:
		case !strings.HasPrefix(l, " "):
			section = k
		default:
			m[section+"."+k] = strings.TrimSpace(v)
		}
	}
	return m
}

// bd checks bd's version and that it finds a store from the project's repo (else here):
// bs bead and the bead checks of note and beads goals read it.
func (d *doctor) bd() {
	at, v, err := d.version("bd")
	if err != nil {
		d.line("FAIL", "bd", "%v", err)
		return
	}
	dir := cmp.Or(d.r.s.Repo, d.r.env.Dir)
	ctx, cancel := context.WithTimeout(d.r.ctx, 5*time.Second)
	defer cancel()
	out, err := d.r.ports.Run.Run(ctx, dir, at, "where")
	var ee *port.ExitError
	switch {
	case errors.As(err, &ee):
		d.line("FAIL", "bd", "%s %s, but bd where finds no store from %s: %v; set BEADS_DIR (docs/SETUP-AGENT.md, step 3)", at, v, dir, err)
		return
	case err != nil:
		d.line("FAIL", "bd", "%s %s, but bd where couldn't run from %s: %v", at, v, dir, err)
		return
	}
	store, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	d.line("ok", "bd", "%s %s, store %s", at, v, store)
}

// review checks what a pr, push or review goal needs of the review host: on GitHub, gh
// logged in and a GitHub remote in the repo (bs spawn refuses those goals without one,
// bees-12.42).
func (d *doctor) review() {
	s := d.r.s
	if s.Review.Host != github.Name {
		return
	}
	at, v, err := d.version("gh")
	switch {
	case err != nil:
		d.line("WARN", "gh", "%v: pr, push and review goals need it; branch, verdict, note and beads don't", err)
	default:
		ctx, cancel := context.WithTimeout(d.r.ctx, 10*time.Second)
		defer cancel()
		var ee *port.ExitError
		if _, err := d.r.ports.Run.Run(ctx, "", at, "auth", "status"); errors.As(err, &ee) {
			d.line("WARN", "gh", "%s %s, but not logged in (%v): run gh auth login", at, v, err)
		} else if err != nil { // couldn't look: never "not logged in" (Rule G)
			d.line("WARN", "gh", "%s %s, but gh auth status couldn't run: %v", at, v, err)
		} else {
			d.line("ok", "gh", "%s %s, logged in", at, v)
		}
	}
	if s.Repo == "" || !s.GitTop {
		return
	}
	rev := d.r.ports.Review
	if rev == nil {
		rev = github.Host{Run: d.r.ports.Run}
	}
	switch repo, ok, err := rev.Repo(d.r.ctx, s.Repo); {
	case err != nil:
		d.line("FAIL", "remote", "can't read %s's git remotes: %v", s.Repo, err)
	case !ok:
		d.line("WARN", "remote", "%s has no GitHub remote: %s spawn refuses --goal pr, push= and review= here (branch, verdict, note and beads work); add one with git remote add origin <url>", s.Repo, bsname.Cmd)
	default:
		d.line("ok", "remote", "%s for pr goals", repo)
	}
}

// unread warns of each key a file sets that bs doesn't act on yet: a guards.deny set
// and ignored would be a guard the keeper thinks is on.
func (d *doctor) unread() {
	unread := map[string][]string{}
	for _, f := range d.r.s.Fields() {
		// [workspace.command] is not ignored: spawn refuses it (the workspace line).
		if f.Unread != "" && f.Source.Kind != "default" && !strings.HasPrefix(f.Key, "workspace.command") {
			unread[f.Unread] = append(unread[f.Unread], f.Key)
		}
	}
	for _, why := range slices.Sorted(maps.Keys(unread)) {
		d.line("WARN", "config", "%s set, but %s: bs ignores it", strings.Join(unread[why], ", "), why)
	}
}

// commands checks that every argv the config names starts a program on the caller's
// PATH: the one bs done, end and prune hand a cmd: check, which runs with only PATH, HOME
// and LANG.
func (d *doctor) commands() {
	argvs := d.r.s.argvs()
	if len(argvs) == 0 {
		return
	}
	missing := 0
	for _, a := range argvs {
		if _, ok := lookPath(d.path, a[1]); !ok {
			d.line("FAIL", a[0], "%s is not on PATH, the one a check runs with", a[1])
			missing++
		}
	}
	if missing == 0 {
		d.line("ok", "commands", "%d argv in the config, each argv[0] on PATH", len(argvs))
	}
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
