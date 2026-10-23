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
	"github.com/jmmshn/bees/internal/checks"
	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/harness/agy"
	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/proc"
	"github.com/jmmshn/bees/internal/review/github"
)

// doctor checks what bs needs, one line each: ok, WARN (bs works, with a caveat) or
// FAIL (a bs command would refuse or misbehave), a FAIL or WARN saying what to do. Any
// FAIL exits 1. r.s.Broken, the error that stopped bs reading the config, is a FAIL of its
// own, and the checks that need no config still run on the defaults.
func (r *run) doctor(fs *flag.FlagSet, args []string) error {
	if err := parse(fs, args); err != nil {
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
		d.line("skip", "project", "project, workspace and logbook dir: %s can't work out the project (above)", bsname.Cmd)
	}
	d.harness()
	d.herdr()
	d.bd()
	if s.Broken == nil {
		d.review()
		d.commands()
		d.unread()
	} else {
		d.line("skip", "review and commands", "gh and the GitHub remote, the config's argv, keys %s doesn't read yet: they need the project (above)", bsname.Cmd)
	}
	stray := 0
	for _, kv := range r.env.Vars {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "BS_") {
			stray++
		}
		switch {
		case !strings.HasPrefix(k, "BS_"):
		case s.Caller.Recorded():
			d.line("WARN", "env", "%s is set under %s, a recorded session, where %s ignores it: %s sets no BS_* in a session's env, so it leaked from the shell that started herdr", k, s.Caller.Name, bsname.Cmd, bsname.Cmd)
		case !slices.Contains(envKeys, k):
			d.line("WARN", "env", "%s is set; %s doesn't read it (an older bs did): it does nothing here", k, bsname.Cmd)
		default:
			d.line("WARN", "env", "%s is set and overrides the config here (from the keeper's shell), for every %s command run from this shell: unset it unless you meant it", k, bsname.Cmd)
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
	"agy":    "1.4.6",   // the version bees-13.80's probes ran on
	"bd":     "1.1.0",   // bd query
}

// version runs program --version, found on the caller's PATH, and returns the first x.y.z
// it prints and where the program is, refusing one older than minVersion.
func (d *doctor) version(program string) (at, v string, err error) {
	at, ok := checks.LookPath(d.path, program)
	if !ok && strings.Contains(program, "/") {
		return "", "", fmt.Errorf("%s is missing or not executable", program)
	} else if !ok {
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
	if min, ok := minVersion[filepath.Base(program)]; ok && older(v, min) {
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
	case s.RepoMissing:
		d.line("FAIL", "repo", "%s (%s) doesn't exist: set paths.repo to the project's checkout", s.Repo, s.RepoFrom)
	default:
		kind, why, _ := s.Kind(s.GitTop)
		d.line("ok", "project", "%s (%s), repo %s, workspace %s (%s)", s.Project, s.ProjectFrom, s.Repo, kind, why)
	}
	// A kind spawn won't make yet is a spawn refused, said here first.
	switch kind, why, _ := s.Kind(s.GitTop); {
	case kind == config.CommandKind && s.Workspace.Command.Incomplete() != nil:
		d.line("FAIL", "workspace", "command (%s): %v: every spawn refuses it", why, s.Workspace.Command.Incomplete())
	case unmade(kind) == "" || s.Repo == "" || s.RepoMissing:
	case kind == config.NoWorkspace && s.GitTop:
		d.line("FAIL", "workspace", "workspace.kind = none over a git checkout (%s): %s, and workers would share %s with no branch of their own (bees-12.28); drop the override for git_worktree", why, unmade(kind), s.Repo)
	default:
		d.line("FAIL", "workspace", "none (%s): %s; make the project a git checkout, or wait for none", why, unmade(kind))
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
	at, v, err := d.version(s.program(s.Harness))
	if err != nil {
		d.line("FAIL", name, "%v", err)
	} else {
		d.line("ok", name, "%s %s", at, v)
	}
	if s.Harness == agy.Name && at != "" {
		d.agyBin(at)
		d.agySignIn(at)
	}
	h := d.r.ports.Harness
	if h == nil && d.r.ports.NewHarness != nil {
		h = d.r.ports.NewHarness(s.Harness, s.opts(s.Harness, false))
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

// agyBin checks that at, agy.bin found, is the agy workers run: herdr's --kind agy starts
// whatever agy the pane's PATH finds (bees-14.7), while bs queen execs agy.bin.
func (d *doctor) agyBin(at string) {
	const how = "herdr's --kind agy"
	switch path, ok := checks.LookPath(d.path, agy.Name); {
	case !ok:
		d.line("FAIL", "agy.bin", "workers start agy from PATH (%s), and this shell's PATH has none: put %s's dir first on it", how, at)
	case !samePath(path, at):
		d.line("WARN", "agy.bin", "workers start %s, the agy on this shell's PATH (%s), not %s, the queen's: put %s's dir first on PATH", path, how, at, at)
	}
}

// agySignIn checks agy at is signed in as a worker's runs, without agy.Remote, which its
// launch unsets: agy reads them as a remote session and says "not signed in" there, and a
// herdr server started over ssh hands them to every pane (bees-13.13.13). agy models asks
// agy's server, so a check that can't tell warns too.
func (d *doctor) agySignIn(at string) {
	argv := []string{"env"}
	for _, v := range agy.Remote {
		argv = append(argv, "-u", v)
	}
	ctx, cancel := context.WithTimeout(d.r.ctx, 20*time.Second)
	defer cancel()
	switch _, err := d.r.ports.Run.Run(ctx, "", append(argv, at, "models")...); {
	case err != nil && strings.Contains(strings.ToLower(err.Error()), "sign in"):
		d.line("WARN", "agy sign-in", "%s isn't signed in as workers run it (without %s), so an agy worker can't start: run agy once to sign in", at, strings.Join(agy.Remote, ", "))
	case err != nil:
		d.line("WARN", "agy sign-in", "can't tell whether workers' agy is signed in: %v", err)
	default:
		d.line("ok", "agy sign-in", "%s models answers without %s", at, strings.Join(agy.Remote, ", "))
	}
}

// herdr checks the session's herdr server: running, its endpoint and private protocol
// compatible with the client, not older than the client nor running a stale binary (bs's
// herdr probes ran on the client's version, bees-12.44), and its socket closed to
// everyone else (T3).
func (d *doctor) herdr() {
	s := d.r.s
	name := "herdr session " + s.Session
	if at, ok := checks.LookPath(d.path, "herdr"); !ok {
		d.line("FAIL", "herdr", "herdr is not on PATH: install it (docs/SETUP-AGENT.md, step 3)")
		return
	} else {
		d.line("ok", "herdr", "%s", at)
	}
	ctx, cancel := context.WithTimeout(d.r.ctx, 5*time.Second)
	defer cancel()
	c := herdr.Client{Run: d.r.ports.Run, Session: s.Session}
	st, err := c.Status(ctx)
	if err != nil {
		d.line("FAIL", name, "%v", err)
		return
	}
	restart := strings.Join(c.Argv("server", "stop"), " ") + ", then start it again"
	switch {
	case st["server.status"] != "running":
		self, _ := os.Executable()
		d.line("FAIL", name, "server %s (socket %s): start it with this build first on its PATH, PATH=%s:$PATH %s, then run %s in one of its panes",
			cmp.Or(st["server.status"], "status unknown"), st["server.socket"], filepath.Dir(self), strings.Join(c.Argv(), " "), bsname.Cmd)
		return
	case st["server.endpoint_compatible"] == "no":
		d.line("FAIL", name, "client %s can't reach server %s's endpoint: restart the server (%s)", st["client.version"], st["server.version"], restart)
	case st["server.private_protocol_compatible"] == "no":
		d.line("FAIL", name, "client %s doesn't speak server %s's private protocol: restart the server (%s)", st["client.version"], st["server.version"], restart)
	case semver.MatchString(st["server.version"]) && semver.MatchString(st["client.version"]) && older(st["server.version"], st["client.version"]):
		d.line("WARN", name, "server %s is older than client %s, still running the binary it started from: restart it (%s) when no session you need runs; bees' herdr probes ran on the client's version", st["server.version"], st["client.version"], restart)
	case st["update.restart_needed"] == "yes":
		d.line("WARN", name, "herdr says its server needs a restart (update.restart_needed): %s, when no session you need runs", restart)
	case st["update.server_binary_stale"] == "yes":
		d.line("WARN", name, "herdr says its server runs a stale binary (update.server_binary_stale): %s, when no session you need runs", restart)
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
	if f := s.hostPrompt(); f != "" {
		switch b, err := os.ReadFile(f); {
		case err != nil:
			d.line("FAIL", "review", "review.fragment: %v: %s spawn refuses pr, push and review goals until it can be read", err, bsname.Cmd)
		case strings.TrimSpace(string(b)) == "":
			d.line("FAIL", "review", "review.fragment %s is blank: %s spawn refuses pr, push and review goals", f, bsname.Cmd)
		default:
			d.line("ok", "review", "the review host's section from %s", f)
		}
	}
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
	ctx, cancel := context.WithTimeout(d.r.ctx, 10*time.Second)
	defer cancel()
	switch repo, ok, err := rev.Repo(ctx, s.Repo); {
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
		if f.Unread != "" && !f.Unset && f.Source.Kind != "default" && !strings.HasPrefix(f.Key, "workspace.command") {
			unread[f.Unread] = append(unread[f.Unread], f.Key)
		}
	}
	for _, why := range slices.Sorted(maps.Keys(unread)) {
		d.line("WARN", "config", "%s set, but %s: %s ignores it", strings.Join(unread[why], ", "), why, bsname.Cmd)
	}
}

// commands checks that every argv the config names, of a key bs reads, starts a
// program: on this shell's PATH (in a pane, the session's), the PATH bs done, end and
// prune hand a cmd: check, which runs with only PATH, HOME and LANG; a relative path from
// the repo top, as a check runs in a worker's worktree. An unread key's argv runs
// nowhere yet, so its missing program is a WARN: it blocks nothing until bs reads it.
func (d *doctor) commands() {
	unread := map[string]bool{}
	for _, f := range d.r.s.Fields() {
		unread[f.Key] = f.Unread != ""
	}
	where := "this shell's PATH (a check runs on the PATH of the " + bsname.Cmd + " that runs it: run doctor in a pane of the session to check that one)"
	if d.r.s.Caller.Recorded() {
		where = "this pane's PATH, the one a check runs with"
	}
	argvs, missing := d.r.s.argvs(), 0
	for _, a := range argvs {
		program := a[1]
		if strings.Contains(program, "/") && !filepath.IsAbs(program) && d.r.s.Repo != "" {
			program = filepath.Join(d.r.s.Repo, program)
		}
		switch _, ok := checks.LookPath(d.path, program); {
		case ok:
		case unread[a[0]]:
			d.line("WARN", a[0], "%s is not on %s; %s doesn't read %s yet, so nothing runs it until it does", a[1], where, bsname.Cmd, a[0])
			missing++
		default:
			d.line("FAIL", a[0], "%s is not on %s", a[1], where)
			missing++
		}
	}
	if len(argvs) > 0 && missing == 0 {
		d.line("ok", "commands", "%d argv in the config, each argv[0] on %s", len(argvs), strings.SplitN(where, " (", 2)[0])
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
		add("workspace.command.branch", c.Branch)
		add("workspace.command.pushed", c.Pushed)
		add("workspace.command.has", c.Has)
		add("workspace.command.descends", c.Descends)
	}
	for _, name := range slices.Sorted(maps.Keys(s.Checks)) {
		add("checks."+name+".run", s.Checks[name].Run)
	}
	return out
}
