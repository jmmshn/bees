package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/checks"
	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/prompt"
	"github.com/jmmshn/bees/internal/vcs/command"
)

// The keeper's commands (ADR-0010 Workspaces and Goals): a custom goal type's cmd: checks
// and a command workspace's create, clean and remove. bs runs the argv config had at spawn,
// frozen in the record, through the checks runner: never a shell, placeholders from the
// record only, a bound on each, its process group killed at the bound.

// parseGoal resolves --goal v: a custom type of the config ([goal.<name>]), frozen with
// each cmd: check's argv and its fragment's hash, or a built-in one. own is the custom
// type's fragment text, when it has one.
func (c *Core) parseGoal(v string) (goal.Goal, prompt.Own, error) {
	typ := goal.TypeOf(v)
	t, ok := c.Goals[typ]
	if !ok {
		g, err := goal.Parse(v)
		g.Fragment = prompt.Hash(g.Type)
		return g, nil, err
	}
	if v != typ {
		return goal.Goal{}, nil, fmt.Errorf("%w %q: the custom type %s takes no :<state> or =<target>", goal.ErrBad, v, typ)
	}
	cmds := map[string]checks.Cmd{}
	for _, n := range slices.Concat(t.Handoff, t.Complete, t.Prune) {
		if name := goal.Cmd(n); name != "" {
			ch, ok := c.Checks[name]
			if !ok { // config refused this at load; a Core built by hand may not have
				return goal.Goal{}, nil, fmt.Errorf("%w %q: %s names no [checks.%s]", goal.ErrBad, v, n, name)
			}
			cmds[name] = checks.Cmd{Argv: slices.Clone(ch.Run), Timeout: duration(ch.Timeout)}
		}
	}
	g := goal.Custom(typ, t.Handoff, t.Complete, t.Prune, cmds)
	if t.Fragment == "" {
		return g, nil, nil
	}
	b, err := os.ReadFile(t.Fragment)
	if err != nil {
		return goal.Goal{}, nil, fmt.Errorf("[goal.%s] fragment: %w", typ, err)
	}
	g.Fragment = prompt.HashText(string(b))
	return g, prompt.Own{typ: string(b)}, nil
}

// duration is a timeout config checked at load; "" is the runner's default.
func duration(s string) time.Duration {
	d, _ := time.ParseDuration(s)
	return d
}

// ownOf is the fragment text recorded goal g's custom type had at spawn: its file read
// again now, refused when it changed since (the worker would work to wording the record
// doesn't hold) or can't be read. nil for a goal with no fragment of the keeper's.
func (c *Core) ownOf(g goal.Goal, name string) (prompt.Own, error) {
	t, ok := c.Goals[g.Type]
	if !ok || goal.IsType(g.Type) || g.Fragment == "" {
		return nil, nil
	}
	next := bsname.Cmd + " goal " + name + " " + g.String() + " (it records the fragment as it is now)"
	if t.Fragment == "" {
		return nil, exit(ExitRefused, next, nil, "%s: [goal.%s] has no fragment any more, and its spawn recorded one", name, g.Type)
	}
	b, err := os.ReadFile(t.Fragment)
	if err != nil {
		return nil, exit(ExitRefused, next, err, "%s: can't read [goal.%s] fragment %s", name, g.Type, t.Fragment)
	}
	if prompt.HashText(string(b)) != g.Fragment {
		return nil, exit(ExitRefused, next, nil, "%s: [goal.%s] fragment %s changed since its goal was recorded", name, g.Type, t.Fragment)
	}
	return prompt.Own{g.Type: string(b)}, nil
}

// queenOwn is the fragment of every custom type the config has, for the queen's prompt;
// one that can't be read is said and left out (the spawn that needs it refuses).
func (c *Core) queenOwn() ([]string, prompt.Own) {
	var types []string
	own := prompt.Own{}
	for _, typ := range slices.Sorted(maps.Keys(c.Goals)) {
		types = append(types, typ)
		f := c.Goals[typ].Fragment
		if f == "" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			c.warn("[goal.%s] fragment: %v", typ, err)
			continue
		}
		own[typ] = string(b)
	}
	return types, own
}

// vals is what a command's placeholders are filled from: the record, never a worker.
func vals(project, name, bead string, ws logbook.Workspace) map[string]checks.Value {
	v := map[string]checks.Value{"dir": {S: ws.Dir}, "name": {S: name}, "bead": {S: bead}, "project": {S: project}, "repo": {S: ws.Repo}}
	if ws.Branch != "" {
		v["branch"] = checks.Value{S: ws.Branch, Ref: true}
	}
	if ws.Base != "" {
		v["base"] = checks.Value{S: ws.Base}
	}
	if ws.Start != "" {
		v["start"] = checks.Value{S: ws.Start}
	}
	return v
}

// getenv is bs's own environment, which a command bs runs takes PATH, HOME and LANG from.
func (c *Core) getenv(key string) string {
	if c.Getenv == nil {
		return os.Getenv(key)
	}
	return c.Getenv(key)
}

// refuseCmds refuses (exit 2) goal g in workspace ws when a command it or the workspace
// would run can't: argv[0] on no PATH a command gets (a relative path with a / is looked
// for only where it runs), or a placeholder this workspace has nothing for ({branch} in a
// workspace with no branch). A custom goal's built-in check that reads a branch is refused
// in a workspace with none. create is the argv a spawn will make ws with; none for an amend
// or a reused workspace. A ref git couldn't check is exit 6.
func (c *Core) refuseCmds(ctx context.Context, g goal.Goal, name, bead string, ws logbook.Workspace, create []string) error {
	if ws.Branch == "" {
		if on := g.OnBranch(); len(on) > 0 {
			return exit(ExitRefused, "", nil, "--goal %s: a %s workspace has no branch for %s to read", g, ws.Kind, strings.Join(on, ", "))
		}
	}
	type named struct {
		key  string
		argv []string
	}
	var all []named
	for _, n := range slices.Sorted(maps.Keys(g.Cmds)) {
		all = append(all, named{"checks." + n + ".run", g.Cmds[n].Argv})
	}
	if len(create) > 0 {
		all = append(all, named{"workspace.command.create", create})
	}
	if ws.Command != nil {
		all = append(all, named{"workspace.command.clean", ws.Command.Clean.Argv}, named{"workspace.command.remove", ws.Command.Remove.Argv})
	}
	v := vals(c.Project, name, bead, ws)
	if ws.Command != nil && ws.Command.VCS != nil { // filled per question, so only the commit placeholders are sample
		x := ws.Command.VCS
		all = append(all, named{"workspace.command.branch", x.Branch.Argv}, named{"workspace.command.pushed", x.Pushed.Argv},
			named{"workspace.command.has", x.Has.Argv}, named{"workspace.command.descends", x.Descends.Argv})
		v["commit"], v["ancestor"] = checks.Value{S: "commit"}, checks.Value{S: "ancestor"}
	}
	for _, a := range all {
		if _, err := checks.Fill(ctx, a.argv, v); errors.Is(err, checks.ErrCantRun) {
			return exit(ExitCantCheck, "", err, "--goal %s: %s", g, a.key)
		} else if err != nil {
			return exit(ExitRefused, bsname.Cmd+" config", err, "--goal %s: %s can't run in a %s workspace", g, a.key, ws.Kind)
		}
		if p := a.argv[0]; !strings.Contains(p, "/") || filepath.IsAbs(p) {
			if _, ok := checks.LookPath(c.getenv("PATH"), p); !ok {
				return exit(ExitRefused, bsname.Cmd+" doctor", nil, "--goal %s: %s: %s is not on PATH; nothing was made", g, a.key, p)
			}
		}
	}
	return nil
}

// vcs is workspace ws's VCS, as the goal checks read it: a command workspace's frozen
// branch, pushed, has and descends, else the configured one (git).
func (c *Core) vcs(ws logbook.Workspace) port.VCS {
	if ws.Command != nil && ws.Command.VCS != nil {
		return command.VCS{Cmds: *ws.Command.VCS, Getenv: c.getenv}
	}
	return c.VCS
}

// runCmd runs check name, a cmd: check of goal g, in worker sp's recorded dir: exit 0 is a
// pass, any other exit a fail naming what it printed; one that couldn't run, or ran past
// its bound, is an error, never a fail (Rule G).
func (k *look) runCmd(ctx context.Context, g goal.Goal, name string) (outcome, string, error) {
	cmd, ok := g.Cmds[goal.Cmd(name)]
	if !ok {
		return failed, "", fmt.Errorf("%s: no argv recorded for it", name)
	}
	r, err := checks.Run(ctx, cmd, k.sp.Workspace.Dir, vals(k.c.Project, k.sp.Name, k.sp.Bead, k.sp.Workspace), k.c.getenv)
	if err != nil {
		return failed, "", err
	}
	if r.Code != 0 {
		return failed, exited(r), nil
	}
	return passed, strings.TrimPrefix(said(r.Output), ": "), nil
}

// exited is a command that exited non-zero, as a check's detail or an error says it: its
// filled argv, its code, and the tail of what it printed.
func exited(r checks.Result) string {
	return fmt.Sprintf("%s exited %d%s", strings.Join(r.Argv, " "), r.Code, said(r.Output))
}

// said is a command's output for a check's detail: its last few lines, ": "-led, or "".
func said(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	s := strings.Join(lines, " / ")
	if s == "" {
		return ""
	}
	return ": " + s
}

// commandWorkspace works out a command workspace for worker name: its dir is config's
// template filled with {repo} and {name}; its clean and remove are frozen. When ended,
// name's last spawn, recorded the same dir and it is still there, it is reused (reused
// true), as a kept worktree is; any other existing dir refuses the spawn: no entry says
// whose it is.
func (c *Core) commandWorkspace(name string, ended *logbook.Spawn) (ws logbook.Workspace, reused bool, err error) {
	cmd := c.Command
	if err := cmd.Incomplete(); err != nil {
		return logbook.Workspace{}, false, exit(ExitRefused, bsname.Cmd+" config", err, "workspace kind command (%s); nothing was made", c.KindWhy)
	}
	dir := config.FillDir(cmd.Dir, c.Repo, name)
	if !filepath.IsAbs(dir) {
		return logbook.Workspace{}, false, exit(ExitRefused, bsname.Cmd+" config", nil, "[workspace.command] dir %q is %s, not an absolute path", cmd.Dir, dir)
	}
	dir = filepath.Clean(dir)
	t := duration(cmd.Timeout)
	ws = logbook.Workspace{Kind: config.CommandKind, Dir: dir, Repo: c.Repo, Command: &logbook.Command{
		Remove: checks.Cmd{Argv: slices.Clone(cmd.Remove), Timeout: t}, Clean: checks.Cmd{Argv: slices.Clone(cmd.Clean), Timeout: t}, VCS: cmd.VCSCmds(t)}}
	if ws.Command.VCS != nil { // the branch its VCS commands are asked about
		ws.Branch = name
	}
	switch _, err := os.Stat(dir); {
	case err == nil && ended != nil && ended.Workspace.Kind == config.CommandKind && samePath(ended.Workspace.Dir, dir):
		ws.Command = ended.Workspace.Command // the commands it was made with remove it, and read it
		ws.Branch = ended.Workspace.Branch
		return ws, true, nil
	case err == nil:
		return logbook.Workspace{}, false, exit(ExitRefused, "", nil, "%s already exists, and no spawn of %s recorded it: look at it, then remove it by hand; nothing was made", dir, name)
	case !errors.Is(err, os.ErrNotExist):
		return logbook.Workspace{}, false, exit(ExitCantCheck, "", err, "can't tell whether %s exists", dir)
	}
	return ws, false, nil
}

// create runs [workspace.command] create in the repo to make ws.Dir for worker name, and
// returns the directory's inode. A create that fails, times out, or exits 0 with no dir
// made runs remove, so nothing is left and nothing recorded (motif 8). The spawn lock
// keeps a twin spawn from making the same dir meanwhile.
func (c *Core) create(ctx context.Context, name, bead string, ws logbook.Workspace) (uint64, error) {
	cmd := checks.Cmd{Argv: c.Command.Create, Timeout: duration(c.Command.Timeout)}
	r, err := checks.Run(ctx, cmd, ws.Repo, vals(c.Project, name, bead, ws), c.getenv)
	switch {
	case err != nil:
	case r.Code != 0:
		err = errors.New(exited(r))
	default:
		switch fi, serr := os.Stat(ws.Dir); {
		case errors.Is(serr, os.ErrNotExist):
			err = fmt.Errorf("exited 0 but made no directory %s", ws.Dir)
		case serr != nil:
			err = fmt.Errorf("exited 0, and %s can't be looked at: %w", ws.Dir, serr)
		case !fi.IsDir():
			err = fmt.Errorf("exited 0 but %s is no directory", ws.Dir)
		default:
			return inode(fi), nil
		}
	}
	if rerr := c.remove(context.WithoutCancel(ctx), name, bead, ws); rerr != nil {
		err = errors.Join(err, fmt.Errorf("and remove: %w", rerr))
	}
	return 0, exit(ExitFail, "", err, "%s: [workspace.command] create %s; nothing was recorded", name, ws.Dir)
}

// inode is fi's inode number, or 0 where the platform gives none.
func inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

// spawnLock holds worker name's spawn lock, from its first look at the record and the
// disk to its record: a twin spawn waits, then finds the spawn and resumes or refuses,
// never making (or removing) the same dir (motif 7). A command workspace's dir is the
// keeper's argv's, which may not fail on one that exists, so only its kind takes it.
func (c *Core) spawnLock(ctx context.Context, name string) (func(), error) {
	dir := filepath.Join(c.CacheDir, "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, exit(ExitFail, "", err, "%s: can't make its spawn lock", name)
	}
	t := c.timing()
	wait := t.Confirm + t.AfterErr + 2*cmp.Or(duration(c.Command.Timeout), checks.DefaultTimeout)
	unlock, err := lock(ctx, filepath.Join(dir, name+".spawn.lock"), wait, t.Poll)
	switch {
	case errors.Is(err, ErrStarting):
		return nil, exit(ExitRefused, bsname.Cmd+" status", nil, "%s: another spawn of it is still under way", name)
	case err != nil:
		return nil, exit(ExitFail, "", err, "%s: its spawn lock", name)
	}
	return unlock, nil
}

// remove runs ws's frozen remove in its repo. A dir still there after a remove that
// exited 0 is an error: the next spawn would refuse it.
func (c *Core) remove(ctx context.Context, name, bead string, ws logbook.Workspace) error {
	r, err := checks.Run(ctx, ws.Command.Remove, ws.Repo, vals(c.Project, name, bead, ws), c.getenv)
	switch {
	case err != nil:
		return err
	case r.Code != 0:
		return errors.New(exited(r))
	}
	switch _, err := os.Stat(ws.Dir); {
	case err == nil:
		return fmt.Errorf("%s exited 0 and left %s", strings.Join(r.Argv, " "), ws.Dir)
	case !errors.Is(err, os.ErrNotExist): // never read as gone (Rule G)
		return fmt.Errorf("%w: %s exited 0, and whether it left %s can't be told: %w", checks.ErrCantRun, strings.Join(r.Argv, " "), ws.Dir, err)
	}
	return nil
}

// clean runs ws's frozen clean in it: exit 0 is nothing there would be lost.
func (k *look) clean(ctx context.Context) (outcome, string, error) {
	ws := k.sp.Workspace
	r, err := checks.Run(ctx, ws.Command.Clean, ws.Dir, vals(k.c.Project, k.sp.Name, k.sp.Bead, ws), k.c.getenv)
	if err != nil {
		return failed, "", err
	}
	if r.Code != 0 {
		return failed, fmt.Sprintf("[workspace.command] clean says %s holds work that would be lost (%s)", ws.Dir, exited(r)), nil
	}
	return passed, "", nil
}

// freeCommandDir removes ended spawn sp's command workspace with its frozen remove, or says
// why it kept it (unknown: it couldn't check): herdr has it open or a pane working in it,
// or its clean exits non-zero. bs removes nothing itself: remove is the keeper's argv.
func (c *Core) freeCommandDir(ctx context.Context, sp logbook.Spawn) (why string, unknown bool) {
	// Only the directory its create made: one remade there by hand is not this spawn's to
	// remove (as freeDir keeps a dir that is no longer a worktree of its repo).
	if ino := sp.Workspace.Command.Ino; ino != 0 {
		switch fi, err := os.Stat(sp.Workspace.Dir); {
		case err != nil:
			return "can't look at it: " + err.Error(), true
		case inode(fi) != ino:
			return "it is no longer the directory its spawn made (made again since?): look at it, then remove it by hand", false
		}
	}
	if why, err := c.inUse(ctx, sp); err != nil {
		return "can't tell whether herdr uses it: " + err.Error(), true
	} else if why != "" {
		return why, false
	}
	if r := c.check(ctx, sp, goal.Clean, Handed{}); r.Err != "" {
		return "can't run its clean: " + r.Err, true
	} else if !r.Pass {
		return r.Detail, false
	}
	if err := c.remove(ctx, sp.Name, sp.Bead, sp.Workspace); err != nil {
		return "[workspace.command] remove: " + err.Error(), errors.Is(err, checks.ErrCantRun)
	}
	return "", false
}

// openCommand makes command workspace ws (unless reused: its last spawn's, kept) and opens
// a herdr workspace on it. A herdr that fails runs remove on what this spawn made, so a
// failure leaves nothing behind and nothing recorded.
func (c *Core) openCommand(ctx context.Context, session string, ws *logbook.Workspace, name, bead, label string, reused bool) (herdr.Workspace, error) {
	if !reused {
		ino, err := c.create(ctx, name, bead, *ws)
		if err != nil {
			return herdr.Workspace{}, err
		}
		ws.Command.Ino = ino
	}
	hw, err := c.herdr(session).CreateWorkspace(ctx, ws.Dir, label)
	if err == nil {
		if reused {
			c.warn("%s: in its kept directory %s, with the work its last spawn left there", name, ws.Dir)
		}
		return hw, nil
	}
	if !reused {
		if rerr := c.remove(context.WithoutCancel(ctx), name, bead, *ws); rerr != nil {
			err = errors.Join(err, fmt.Errorf("and [workspace.command] remove: %w", rerr))
		}
	}
	return herdr.Workspace{}, exit(ExitFail, "", err, "%s: herdr couldn't open a workspace on %s; nothing was recorded", name, ws.Dir)
}
