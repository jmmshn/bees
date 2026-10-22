package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/jmmshn/bees/cmd/bsx/internal/bsname"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/goal"
	"github.com/jmmshn/bees/cmd/bsx/internal/herdr"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// SpawnReq is what bs spawn takes from its caller: only what bs can't look up (Rule D).
type SpawnReq struct {
	By      string // the queen
	Session string // the herdr session the spawn opens on: the queen's
	Bead    string
	Label   string   // "" is the bead's title
	Goals   []string // --goal, required
}

// Spawn starts a worker for req.Bead: it resolves the goal, opens its git_worktree
// workspace through herdr, records the spawn with its brief, launches the harness, and
// confirms the start. Nothing is recorded until the workspace exists; a launch that fails
// after the record leaves an unconfirmed spawn (exit 8) that bs end clears.
func (c *Core) Spawn(ctx context.Context, req SpawnReq) (logbook.Spawn, error) {
	g, err := goal.Parse(req.Goals)
	if err != nil {
		return logbook.Spawn{}, exit(ExitRefused, "", err, bsname.Cmd+" spawn %s", req.Bead)
	}
	bead, err := c.Beads.Show(ctx, req.Bead)
	if err != nil {
		if errors.Is(err, port.ErrNoBead) {
			return logbook.Spawn{}, exit(ExitRefused, "bd show "+req.Bead, err, bsname.Cmd+" spawn: no bead %s", req.Bead)
		}
		return logbook.Spawn{}, exit(ExitCantCheck, "", err, bsname.Cmd+" spawn: can't read bead %s", req.Bead)
	}
	name := Name(bead.ID)
	switch prev, ok, err := c.Logbook.LatestSpawn(ctx, name); {
	case err != nil:
		return logbook.Spawn{}, err
	case ok && !prev.Ended:
		return logbook.Spawn{}, exit(ExitRefused, bsname.Cmd+" end "+name, nil, bsname.Cmd+" spawn: %s is already spawned and not ended", name)
	}
	if err := c.refuse(ctx, g); err != nil {
		return logbook.Spawn{}, err
	}
	ws, err := c.workspace(ctx, name)
	if err != nil {
		return logbook.Spawn{}, err
	}
	// Prepare is pure, so it runs before anything is made: a refused launch leaves nothing.
	launch, err := c.Harness.Prepare(port.Start{Role: port.Worker, Name: name, Dir: ws.Dir, Doorbell: doorbell(name, c.Project)})
	if err != nil {
		return logbook.Spawn{}, exit(ExitRefused, "", err, bsname.Cmd+" spawn %s: its harness refused the launch; nothing was made", name)
	}
	label := cmp.Or(req.Label, bead.Title)
	session := cmp.Or(req.Session, logbook.DefaultSession)
	hw, err := c.herdr(session).CreateWorktree(ctx, ws.Repo, ws.Base, ws.Branch, ws.Dir, c.Project+" · "+label)
	if err != nil {
		return logbook.Spawn{}, exit(ExitFail, "", err, bsname.Cmd+" spawn %s: herdr couldn't make its worktree; nothing was recorded", name)
	}
	ws.Dir = hw.Path // herdr makes it where asked; its answer is the record
	sp := logbook.Spawn{
		Name: name, By: req.By, Bead: bead.ID, Label: label, Harness: c.HarnessName,
		Herdr: logbook.Pane{Session: session, Workspace: hw.ID, Pane: hw.Pane}, Workspace: ws,
		Goal: g, ConfigDir: c.ConfigDir,
	}
	sp.Record = launch.Record
	if sp.ID, err = c.Logbook.RecordSpawn(ctx, req.By, sp, brief(bead, g)); err != nil {
		// No record: take back the worktree this spawn made, or a second spawn would find
		// it with nothing to prune it (motif 8).
		// The branch goes too: spawn found none, so this one holds nothing but its base.
		rctx := context.WithoutCancel(ctx)
		if rerr := c.herdr(session).RemoveWorktree(rctx, hw.ID, true); rerr != nil {
			err = errors.Join(err, fmt.Errorf("and its worktree %s stays: %w", ws.Dir, rerr))
		} else if rerr := c.dropBranch(rctx, ws.Repo, ws.Branch); rerr != nil {
			err = errors.Join(err, fmt.Errorf("and its branch %s stays: %w", ws.Branch, rerr))
		}
		if errors.Is(err, logbook.ErrAlreadySpawned) {
			return logbook.Spawn{}, exit(ExitRefused, bsname.Cmd+" end "+name, err, bsname.Cmd+" spawn: another spawn of %s was recorded first", name)
		}
		return logbook.Spawn{}, exit(ExitFail, "", err, bsname.Cmd+" spawn %s: couldn't record the spawn", name)
	}
	if err := c.launch(ctx, sp, launch); err != nil {
		return sp, err
	}
	return sp, nil
}

// refuse runs the spawn-time refusals bs can make now (Rule G). The network ones (a PR
// goal's remote, a push or review target) arrive with bees-13.6.
func (c *Core) refuse(ctx context.Context, g goal.Goal) error {
	for _, p := range g.Parts {
		switch p.Type {
		case "pr", "push", "review":
			return exit(ExitRefused, "", nil, bsname.Cmd+" spawn --goal %s: PR goals arrive with bees-13.6", p)
		case "note", "beads":
			return exit(ExitRefused, "", nil, bsname.Cmd+" spawn --goal %s: arrives with bees-13.7", p)
		}
	}
	return nil
}

// workspace works out the spawn's workspace (Rule D): the project dir the top level of a
// git checkout is git_worktree. none and command arrive with bees-13.10 and 13.11.
func (c *Core) workspace(ctx context.Context, name string) (logbook.Workspace, error) {
	top, err := c.git(ctx, c.Repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return logbook.Workspace{}, refusedOrCant(err, "", bsname.Cmd+" spawn: %s is not a git checkout (workspace none arrives with bees-13.10)", c.Repo)
	}
	real, err := filepath.EvalSymlinks(c.Repo)
	if err != nil {
		return logbook.Workspace{}, exit(ExitCantCheck, "", err, bsname.Cmd+" spawn: can't resolve paths.repo")
	}
	if top != c.Repo && top != real {
		return logbook.Workspace{}, exit(ExitRefused, "", nil, bsname.Cmd+" spawn: %s is inside the checkout %s, not its top level; set paths.repo to it", c.Repo, top)
	}
	base, err := c.git(ctx, c.Repo, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return logbook.Workspace{}, refusedOrCant(err, "git -C "+c.Repo+" switch main", bsname.Cmd+" spawn: %s has no branch checked out to cut from", c.Repo)
	}
	dir := filepath.Join(c.Worktrees, name)
	if _, err := os.Stat(dir); err == nil {
		return logbook.Workspace{}, exit(ExitRefused, bsname.Cmd+" prune", nil, bsname.Cmd+" spawn: %s already exists", dir)
	}
	// The branch is cut fresh: one left by an earlier worker would fail herdr's
	// worktree add after the checks, or hand this worker its commits.
	switch _, err := c.git(ctx, c.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); {
	case err == nil:
		return logbook.Workspace{}, exit(ExitRefused, bsname.Cmd+" prune (it deletes a merged worker's branch), or git -C "+c.Repo+" branch -d "+name, nil,
			bsname.Cmd+" spawn: %s already has a branch %s", c.Repo, name)
	case !isExit(err, 1):
		return logbook.Workspace{}, exit(ExitCantCheck, "", err, bsname.Cmd+" spawn: can't tell whether %s has a branch %s", c.Repo, name)
	}
	return logbook.Workspace{Kind: "git_worktree", Dir: dir, Repo: c.Repo, Branch: name, Base: base}, nil
}

// launch writes the start's files, starts the harness in the recorded pane, and confirms
// the start. herdr's agent_pane_busy (a new pane's shell not at its prompt) is retried
// (bees-12.33). A pane still busy after the retries, or a name herdr says is taken, means
// herdr started nothing; any other start error still looks for the process, since herdr
// can give up while it runs (bees-12.49, adopted).
func (c *Core) launch(ctx context.Context, sp logbook.Spawn, l port.Launch) error {
	args, err := c.writeFiles(sp.Name, l)
	if err != nil {
		return exit(ExitStart, bsname.Cmd+" end "+sp.Name+" --incomplete", err, bsname.Cmd+" spawn %s: couldn't write its start files", sp.Name)
	}
	h := c.herdr(sp.Herdr.Session)
	if err := herdr.CheckAgentArgs(args); err != nil {
		return exit(ExitStart, bsname.Cmd+" end "+sp.Name+" --incomplete", err, bsname.Cmd+" spawn %s: the launch can't be typed into a pane", sp.Name)
	}
	var startErr error
	for try := 0; try < 4; try++ {
		if startErr = h.StartAgent(ctx, sp.Herdr.Pane, sp.Name, c.Harness.Kind(), args...); !errors.Is(startErr, herdr.ErrPaneBusy) {
			break
		}
		if err := sleep(ctx, time.Duration(try+1)*250*time.Millisecond); err != nil {
			return exit(ExitStart, bsname.Cmd+" end "+sp.Name+" --incomplete", err, bsname.Cmd+" spawn %s: interrupted", sp.Name)
		}
	}
	if errors.Is(startErr, herdr.ErrPaneBusy) || errors.Is(startErr, herdr.ErrNameTaken) {
		return exit(ExitStart, bsname.Cmd+" end "+sp.Name+" --incomplete", startErr, bsname.Cmd+" spawn %s: herdr started nothing", sp.Name)
	}
	if err := c.confirm(ctx, sp); err != nil {
		return exit(ExitStart, bsname.Cmd+" end "+sp.Name+" --incomplete", errors.Join(startErr, err), bsname.Cmd+" spawn %s: start unconfirmed", sp.Name)
	}
	return nil
}

// writeFiles writes l.Files to the cache dir as <name>-<key>-* (0600), replacing each
// {{key}} in l.Args with its path, after removing this session's earlier files.
func (c *Core) writeFiles(name string, l port.Launch) ([]string, error) {
	args := slices.Clone(l.Args)
	if len(l.Files) == 0 {
		return args, nil
	}
	old, err := filepath.Glob(filepath.Join(c.CacheDir, name+"-[a-z]*-*"))
	if err != nil {
		return nil, err
	}
	for _, f := range old {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	if err := os.MkdirAll(c.CacheDir, 0o700); err != nil {
		return nil, err
	}
	for _, key := range slices.Sorted(maps.Keys(l.Files)) {
		stem, ext := key, filepath.Ext(key)
		stem = strings.TrimSuffix(stem, ext)
		f, err := os.CreateTemp(c.CacheDir, name+"-"+strings.ReplaceAll(stem, "_", "-")+"-*"+ext)
		if err != nil {
			return nil, err
		}
		_, err = f.WriteString(l.Files[key])
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
		for i := range args {
			args[i] = strings.ReplaceAll(args[i], "{{"+key+"}}", f.Name())
		}
	}
	return args, nil
}

// confirm polls the recorded pane for the harness process named for this worker (its
// kind, with --name <worker> in its argv) and records its start with the baseline of its
// descendants at first sight (ADR-0010).
func (c *Core) confirm(ctx context.Context, sp logbook.Spawn) error {
	t := c.timing()
	h := c.herdr(sp.Herdr.Session)
	var last error
	for deadline := time.Now().Add(t.Confirm); time.Now().Before(deadline); _ = sleep(ctx, t.Poll) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pi, err := h.ProcessInfo(ctx, sp.Herdr.Pane)
		if err != nil {
			last = err
			continue
		}
		for _, p := range pi.Foreground {
			if p.Name != c.Harness.Kind() || !named(p.Argv, sp.Name) {
				continue
			}
			start, err := c.Proc.Start(p.PID)
			if err != nil {
				last = err
				continue
			}
			s := logbook.Start{Spawn: sp.ID, Name: sp.Name, Process: logbook.Process{PID: p.PID, Start: start}, Harness: sp.Harness}
			s.Exe, _ = c.Proc.Exe(p.PID)
			if s.Baseline, err = c.Proc.Descendants(p.PID); err != nil {
				s.BaselineErr = err.Error()
			}
			_, err = c.Logbook.RecordStart(ctx, s)
			return err
		}
	}
	if last == nil {
		last = fmt.Errorf("no %s process named %s in pane %s within %s", c.Harness.Kind(), sp.Name, sp.Herdr.Pane, t.Confirm)
	}
	return last
}

// named reports whether argv carries --name name.
func named(argv []string, name string) bool {
	i := slices.Index(argv, "--name")
	return i >= 0 && i+1 < len(argv) && argv[i+1] == name
}

func brief(b port.Bead, g goal.Goal) string {
	return fmt.Sprintf("You own %s: %s\n\nYour goal: --goal %s. Hand it off with "+bsname.Cmd+" done when it holds.\n\n%s", b.ID, b.Title, g, b.Description)
}

func doorbell(name, project string) string {
	return fmt.Sprintf("["+bsname.Cmd+"] you are %s in %s — run: "+bsname.Cmd+" inbox", name, project)
}

// dropBranch deletes branch from repo if it is still there (herdr's worktree remove may
// have taken it already). Callers have proven it holds nothing to keep.
func (c *Core) dropBranch(ctx context.Context, repo, branch string) error {
	switch _, err := c.git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); {
	case isExit(err, 1):
		return nil
	case err != nil:
		return err
	}
	_, err := c.git(ctx, repo, "branch", "-D", branch)
	return err
}

// isExit reports whether err is a command that ran and exited with code.
func isExit(err error, code int) bool {
	var ee *port.ExitError
	return errors.As(err, &ee) && ee.Code == code
}

// refusedOrCant is a refusal (exit 2) when the tool ran and said no, and "can't check"
// (exit 6) when it couldn't run: a tool that is down is never read as "no" (Rule G).
func refusedOrCant(err error, next, format string, a ...any) *Exit {
	var ee *port.ExitError
	if errors.As(err, &ee) {
		return exit(ExitRefused, next, err, format, a...)
	}
	return exit(ExitCantCheck, "", err, format, a...)
}
