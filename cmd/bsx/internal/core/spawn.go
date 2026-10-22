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

	"github.com/jmmshn/bees/cmd/bsx/internal/bsname"
	"github.com/jmmshn/bees/cmd/bsx/internal/goal"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
	"github.com/jmmshn/bees/internal/prompt"
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
		return logbook.Spawn{}, exit(ExitRefused, "", err, "%s", req.Bead)
	}
	bead, err := c.Beads.Show(ctx, req.Bead)
	if err != nil {
		if errors.Is(err, port.ErrNoBead) {
			return logbook.Spawn{}, exit(ExitRefused, "bd show "+req.Bead, err, "no bead %s", req.Bead)
		}
		return logbook.Spawn{}, exit(ExitCantCheck, "", err, "can't read bead %s", req.Bead)
	}
	name := Name(bead.ID)
	switch prev, ok, err := c.Logbook.LatestSpawn(ctx, name); {
	case err != nil:
		return logbook.Spawn{}, err
	case ok && !prev.Ended:
		return logbook.Spawn{}, exit(ExitRefused, bsname.Cmd+" end "+name, nil, "%s is already spawned and not ended", name)
	}
	if err := c.refuse(ctx, g); err != nil {
		return logbook.Spawn{}, err
	}
	ws, err := c.workspace(ctx, name)
	if err != nil {
		return logbook.Spawn{}, err
	}
	// Prepare is pure, so it runs before anything is made: a refused launch leaves nothing.
	launch, err := c.Harness.Prepare(port.Start{Role: port.Worker, Name: name, Dir: ws.Dir, PromptFile: "{{prompt.md}}", Doorbell: doorbell(name, bead.ID, req.By), Path: c.WorkerPath})
	if err != nil {
		return logbook.Spawn{}, exit(ExitRefused, "", err, "%s: its harness refused the launch; nothing was made", name)
	}
	launch.Files = withPrompt(launch.Files, prompt.Worker, c.Project)
	label := cmp.Or(req.Label, bead.Title)
	session := cmp.Or(req.Session, logbook.DefaultSession)
	hw, err := c.herdr(session).CreateWorktree(ctx, ws.Repo, ws.Base, ws.Branch, ws.Dir, c.Project+" · "+label)
	if err != nil {
		return logbook.Spawn{}, exit(ExitFail, "", err, "%s: herdr couldn't make its worktree; nothing was recorded", name)
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
		if rerr := c.herdr(session).RemoveWorktree(context.WithoutCancel(ctx), hw.ID, true); rerr != nil {
			err = errors.Join(err, fmt.Errorf("and its worktree %s stays: %w", ws.Dir, rerr))
		}
		return logbook.Spawn{}, exit(ExitFail, "", err, "%s: couldn't record the spawn", name)
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
			return exit(ExitRefused, "", nil, "--goal %s: PR goals arrive with bees-13.6", p)
		case "note", "beads":
			return exit(ExitRefused, "", nil, "--goal %s: arrives with bees-13.7", p)
		}
	}
	return nil
}

// workspace works out the spawn's workspace (Rule D): the project dir the top level of a
// git checkout is git_worktree. none and command arrive with bees-13.10 and 13.11.
func (c *Core) workspace(ctx context.Context, name string) (logbook.Workspace, error) {
	if c.Repo == "" {
		return logbook.Workspace{}, exit(ExitRefused, "", nil, "project %s has no repo: run it in the repo, or set paths.repo", c.Project)
	}
	top, err := c.git(ctx, c.Repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return logbook.Workspace{}, refusedOrCant(err, "", "%s is not a git checkout (workspace none arrives with bees-13.10)", c.Repo)
	}
	real, err := filepath.EvalSymlinks(c.Repo)
	if err != nil {
		return logbook.Workspace{}, exit(ExitCantCheck, "", err, "can't resolve paths.repo")
	}
	if top != c.Repo && top != real {
		return logbook.Workspace{}, exit(ExitRefused, "", nil, "%s is inside the checkout %s, not its top level; set paths.repo to it", c.Repo, top)
	}
	base, err := c.git(ctx, c.Repo, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return logbook.Workspace{}, exit(ExitRefused, "git -C "+c.Repo+" switch main", err, "%s has no branch checked out to cut from", c.Repo)
	}
	dir := filepath.Join(c.Worktrees, name)
	if _, err := os.Stat(dir); err == nil {
		return logbook.Workspace{}, exit(ExitRefused, bsname.Cmd+" prune", nil, "%s already exists", dir)
	}
	return logbook.Workspace{Kind: "git_worktree", Dir: dir, Repo: c.Repo, Branch: name, Base: base}, nil
}

// writeFiles writes l.Files to the cache dir as <name>-<key>-* (0600), replacing each
// {{key}} in l.Args with its path, after removing this session's earlier files.
func (c *Core) writeFiles(name string, l port.Launch) ([]string, error) {
	args := slices.Clone(l.Args)
	if len(l.Files) == 0 {
		return args, nil
	}
	if err := c.removeFiles(name); err != nil {
		return nil, err
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

func brief(b port.Bead, g goal.Goal) string {
	return fmt.Sprintf("You own %s: %s\n\nYour goal: --goal %s. Hand it off with "+bsname.Cmd+" done when it holds.\n\n%s", b.ID, b.Title, g, b.Description)
}

// doorbell is a worker's first prompt: who it is, and where to read and reply.
func doorbell(name, bead, queen string) string {
	return fmt.Sprintf(`[%s] you are %s, working on %s — run: %s inbox   (reply with: %s send %s "...")`, bsname.Cmd, name, bead, bsname.Cmd, bsname.Cmd, queen)
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
