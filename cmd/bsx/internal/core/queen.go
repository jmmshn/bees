package core

import (
	"context"
	"fmt"
	"maps"

	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
	"github.com/jmmshn/bees/cmd/bsx/internal/prompt"
)

// QueenReq is bs queen's caller: the pane it runs in and its own process, which exec
// keeps as the harness's.
type QueenReq struct {
	Session, Pane string
	Self          int
	Exe           string // the harness program exec runs, resolved on the caller's PATH
}

// Queen records the project's queen in req's pane and returns the argv bs queen execs:
// the harness, in the repo, with the queen's prompt. It refuses while a queen's recorded
// process lives (motif 7): the check and the record share one transaction. A queen
// recorded before an exec that then fails is harmless: her pid is dead.
func (c *Core) Queen(ctx context.Context, req QueenReq) ([]string, error) {
	name := Queen(c.Project)
	if req.Pane == "" {
		return nil, exit(ExitRefused, "", nil, "bs queen: not in a herdr pane (HERDR_PANE_ID is unset): start the queen in a herdr pane, so her workers can find and ring her")
	}
	if c.Harness == nil {
		return nil, exit(ExitRefused, "bs config", nil, "bs queen: no harness adapter for this config")
	}
	if c.Repo == "" {
		return nil, exit(ExitRefused, "cd into the project's repo, or set paths.repo", nil, "bs queen: project %s has no repo to start her in", c.Project)
	}
	running := func(q logbook.Queen) (bool, error) {
		up, err := c.Proc.Alive(q.Process)
		if err != nil {
			return false, fmt.Errorf("can't tell whether %s still runs (pid %d): %w", q.Name, q.PID, err)
		}
		return up, nil
	}
	// A refused second queen leaves the first's files alone: they are written only once
	// RecordQueen, which checks again under its lock, has recorded this one.
	if q, ok, err := c.Logbook.LatestQueen(ctx, name); err != nil {
		return nil, err
	} else if ok {
		if up, err := running(q); err != nil {
			return nil, exit(ExitCantCheck, "", err, "bs queen")
		} else if up {
			return nil, exit(ExitRefused, "", nil, "bs queen: %s already runs (pid %d, pane %s on herdr session %s); a second queen would race her spawns", name, q.PID, q.Pane, q.Session)
		}
	}
	start, err := c.Proc.Start(req.Self)
	if err != nil {
		return nil, exit(ExitCantCheck, "", err, "bs queen: can't read its own start time")
	}
	l, err := c.Harness.Prepare(port.Start{Role: port.Queen, Name: name, Dir: c.Repo, PromptFile: "{{prompt.md}}"})
	if err != nil {
		return nil, exit(ExitRefused, "", err, "bs queen: its harness refused the launch")
	}
	q := logbook.Queen{Name: name, Harness: c.HarnessName, Session: req.Session, Pane: req.Pane,
		Process: logbook.Process{PID: req.Self, Start: start}, Exe: req.Exe}
	switch prev, ok, err := c.Logbook.RecordQueen(ctx, q, running); {
	case err != nil:
		return nil, err
	case !ok:
		return nil, exit(ExitRefused, "", nil, "bs queen: %s already runs (pid %d, pane %s)", name, prev.PID, prev.Pane)
	}
	l.Files = withPrompt(l.Files, prompt.Text(prompt.Queen, c.Project, c.Kind, prompt.Goals())+prompt.Host(prompt.Queen, c.Review.Name()))
	args, err := c.writeFiles(name, l)
	if err != nil {
		return nil, exit(ExitFail, "bsx queen", err, "bs queen: couldn't write its start files (her entry names this pid, which exits: harmless)")
	}
	return append([]string{c.Harness.Kind()}, args...), nil
}

// withPrompt is files with text, the assembled prompt, added as prompt.md, the file a
// Start's PromptFile "{{prompt.md}}" names: core assembles the prompt, the harness only
// points at it (ADR-0010).
func withPrompt(files map[string]string, text string) map[string]string {
	files = maps.Clone(files)
	if files == nil {
		files = map[string]string{}
	}
	files["prompt.md"] = text
	return files
}
