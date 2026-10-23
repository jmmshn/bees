package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/goal"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/prompt"
)

// QueenReq is bs queen's caller: the pane it runs in and its own process, which exec
// keeps as the harness's.
type QueenReq struct {
	Session, Pane string
	Self          int
	Exe           string // the harness program exec runs, resolved on the caller's PATH
}

// Queen records the project's queen in req's pane and returns the argv bs queen execs:
// the harness, in the repo, with the queen's prompt. The caller has refused a call from
// outside a pane, or with no harness. It refuses while a queen's recorded process lives
// (motif 7): the check and the record share one transaction. A queen recorded before an
// exec that then fails is harmless: her pid is dead.
func (c *Core) Queen(ctx context.Context, req QueenReq) ([]string, error) {
	name := Queen(c.Project)
	if c.Repo == "" {
		return nil, exit(ExitRefused, "cd into the project's repo, or set paths.repo", nil, "project %s has no repo to start her in", c.Project)
	}
	var checkErr error // running's failure, told apart from a logbook write's
	running := func(q logbook.Queen) (bool, error) {
		up, err := c.Proc.Alive(q.Process)
		if err != nil {
			checkErr = fmt.Errorf("can't tell whether %s still runs (pid %d): %w", q.Name, q.PID, err)
			return false, checkErr
		}
		return up, nil
	}
	twin := func(q logbook.Queen) error {
		return exit(ExitRefused, "use her (herdr --session "+q.Session+" pane read "+q.Pane+"), or exit her first", nil,
			"%s already runs (pid %d, pane %s on herdr session %s); a second queen would race her spawns", name, q.PID, q.Pane, q.Session)
	}
	// A refused second queen leaves the first's files alone: they are written only once
	// RecordQueen, which checks again under its lock, has recorded this one.
	if q, ok, err := c.Logbook.LatestQueen(ctx, name); err != nil {
		return nil, err
	} else if ok {
		if up, err := running(q); err != nil {
			return nil, exit(ExitCantCheck, bsname.Cmd+" doctor", err, "can't tell whether a queen already runs")
		} else if up {
			return nil, twin(q)
		}
	}
	start, err := c.Proc.Start(req.Self)
	if err != nil {
		return nil, exit(ExitCantCheck, bsname.Cmd+" doctor", err, "can't read its own start time")
	}
	host, err := c.host(prompt.Queen)
	if err != nil {
		return nil, exit(ExitRefused, bsname.Cmd+" config", err, "her review host's section can't be read")
	}
	text := prompt.Text(prompt.Queen, c.Project, c.Harness.Kind(), c.Kind, goal.BuiltinTypes()) + host
	l, err := c.Harness.Prepare(port.Start{Role: port.Queen, Name: name, Dir: c.Repo, PromptFile: "{{prompt.md}}", Prompt: text})
	if err != nil {
		return nil, exit(ExitRefused, bsname.Cmd+" config", err, "its harness refused the launch")
	}
	q := logbook.Queen{Name: name, Harness: c.HarnessName, Session: req.Session, Pane: req.Pane,
		Process: logbook.Process{PID: req.Self, Start: start}, Exe: req.Exe, Repo: c.Repo}
	switch prev, ok, err := c.Logbook.RecordQueen(ctx, q, running); {
	case checkErr != nil:
		return nil, exit(ExitCantCheck, bsname.Cmd+" doctor", checkErr, "can't tell whether a queen already runs")
	case err != nil:
		return nil, exit(ExitFail, bsname.Cmd+" doctor", err, "couldn't record %s", name)
	case !ok:
		return nil, twin(prev)
	}
	l.Files = withPrompt(l.Files, text)
	args, err := c.writeFiles(name, l)
	if err != nil {
		return nil, exit(ExitFail, bsname.Cmd+" queen", err, "couldn't write her start files (her entry names this pid, which exits: harmless)")
	}
	return append([]string{c.Harness.Kind()}, args...), nil
}

// host is r's review host section of the prompt: review.fragment's file when the keeper
// names one, else the host's built-in fragment. A file that can't be read, or is blank,
// is an error: a session never starts without the instructions it was configured to get.
func (c *Core) host(r prompt.Role) (string, error) {
	if c.HostPrompt == "" {
		return prompt.Host(r, c.Review.Name(), ""), nil
	}
	b, err := os.ReadFile(c.HostPrompt)
	if err == nil && strings.TrimSpace(string(b)) == "" {
		err = errors.New("it is blank")
	}
	if err != nil {
		return "", fmt.Errorf("review.fragment %s: %w", c.HostPrompt, err)
	}
	return prompt.Host(r, c.Review.Name(), string(b)), nil
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
