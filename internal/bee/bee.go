// Package bee starts and ends bees: one Claude Code session per bead, each in its
// own herdr workspace and git worktree (ADR-0006).
package bee

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/project"
	"github.com/jmmshn/bees/internal/prompt"
)

var (
	ErrRunning = errors.New("bee is already running")
	ErrOrphan  = errors.New("a herdr workspace is open on this bee's worktree but bs has no record of it")
	ErrWaiting = errors.New("claude is waiting at a prompt") // started, but needs an answer in its pane
	// ErrUnconfirmed: herdr stopped waiting for Claude to be ready. Claude may be busy
	// with its first prompt or may have exited; only Claude's registry can tell.
	ErrUnconfirmed = errors.New("herdr could not confirm that claude started")
	ErrWrongRepo   = errors.New("bead does not belong to this project")
	ErrNotSpawned  = errors.New("bs has no record of spawning this bee")
	ErrNoLabel     = errors.New("a first spawn needs a label")
	ErrBadLabel    = errors.New("not a valid label")
	ErrLabelChange = errors.New("the bee is recorded with another label")
)

// LabelRules says what a bee's label must be. The queen writes the label; bs only checks it.
const LabelRules = `a label names the work for the herdr sidebar: 1-4 lowercase words of a-z and 0-9, single spaces, no punctuation, at most 32 characters, specific to the bead.
  good: "role prompts", "squash merge end", "natural workspace labels"
  bad:  "fix bug", "update docs", "Role-Prompts."`

var labelRule = regexp.MustCompile(`^[a-z0-9]+( [a-z0-9]+){0,3}$`)

// CheckLabel refuses a label that breaks LabelRules; it never rewrites one.
func CheckLabel(label string) error {
	if len(label) > 32 || !labelRule.MatchString(label) {
		return fmt.Errorf("%q: %w", label, ErrBadLabel)
	}
	return nil
}

// Workspace is a herdr workspace opened on a git worktree.
type Workspace struct{ ID, Label, Pane, Path string }

// Bead is the part of a bead bs reads.
type Bead struct{ ID, Title, Description string }

// Herdr is the slice of herdr bs drives.
type Herdr interface {
	CreateWorktree(ctx context.Context, repo, branch, path, label string) (Workspace, error)
	// StartClaude runs claude with args in pane. ErrWaiting: blocked at a startup prompt.
	// ErrUnconfirmed: herdr gave up waiting. ErrUnsafeArg: an argument herdr can't pass.
	StartClaude(ctx context.Context, pane, name string, args ...string) error
	RemoveWorktree(ctx context.Context, workspace string) error // already gone → nil
	Workspaces(ctx context.Context) ([]Workspace, error)
}

// Sessions tells whether a Claude session is running, from Claude's own registry
// (ADR-0004), not from herdr: herdr leaves an agent unnamed when its start times out.
type Sessions interface {
	// Alive reports whether a session called name is live, waiting up to wait for one
	// to appear.
	Alive(ctx context.Context, name string, wait time.Duration) (bool, error)
}

// Beads reads beads.
type Beads interface {
	Show(ctx context.Context, id string) (Bead, error)
}

// Fleet spawns and ends the bees of one project.
type Fleet struct {
	P           project.Project
	LB          *logbook.Logbook
	H           Herdr
	S           Sessions
	B           Beads
	G           GitHub // nil: End proves a merge from git alone
	Me          string // the queen doing the spawning
	ForeignRepo bool   // skip the check that the bead belongs to P (--repo)
}

var herdrName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Name is the bee's one name for a bead: session, herdr agent, branch, worktree dir
// and address. Bead ids are Jira-style, <project>-<n>[.<m>…] (R27), and project names
// have no "-", so the name is the id with "." → "-": bees-12.3 → bees-12-3.
func Name(bead string) (string, error) {
	n := strings.ReplaceAll(bead, ".", "-")
	if !herdrName.MatchString(n) {
		return "", fmt.Errorf("%q can't be a bee name (lowercase, starting with a letter, at most 32 characters)", bead)
	}
	return n, nil
}

var beadNumber = regexp.MustCompile(`^-\d+(\.\d+)*$`)

// Belongs reports whether bead is one of project's: <project>-<n>[.<m>…].
func Belongs(project, bead string) bool {
	rest, ok := strings.CutPrefix(bead, project)
	return ok && beadNumber.MatchString(rest)
}

// NextEpic returns the next free top-level id for project among ids: <project>-<max+1>.
func NextEpic(project string, ids []string) string {
	top := 0
	for _, id := range ids {
		rest, ok := strings.CutPrefix(id, project+"-")
		rest, _, _ = strings.Cut(rest, ".") // a child proves its epic's number is taken
		if n, err := strconv.Atoi(rest); ok && err == nil && n > top {
			top = n
		}
	}
	return fmt.Sprintf("%s-%d", project, top+1)
}

// Started describes a bee Spawn started.
type Started struct {
	Name, Label, Workspace string
	Resumed                bool // restarted in its recorded workspace
	Continued              bool // resumed with its last conversation (claude --continue)
	Waiting                bool // Claude is waiting at a prompt (e.g. folder trust) in its pane
}

// Spawn starts a bee for bead with label as its herdr workspace label, or restarts it
// in its recorded workspace if its session has died. A restart keeps the recorded
// label: label may be empty, and any other label is refused.
func (f Fleet) Spawn(ctx context.Context, bead, label string) (Started, error) {
	name, err := Name(bead)
	if err != nil {
		return Started{}, err
	}
	if label != "" {
		if err := CheckLabel(label); err != nil {
			return Started{}, err
		}
	}
	sp, ok, err := f.LB.LatestSpawn(ctx, name)
	if err != nil {
		return Started{}, err
	}
	if ok && !sp.Ended {
		if label != "" && label != sp.Label {
			return Started{}, fmt.Errorf("%s is labelled %q, not %q: %w; spawn it again without --label", name, sp.Label, label, ErrLabelChange)
		}
		// Fail closed: an unreadable registry must not start a second Claude.
		if alive, err := f.S.Alive(ctx, name, 0); err != nil {
			return Started{}, err
		} else if alive {
			return Started{}, fmt.Errorf("%s: %w", name, ErrRunning)
		}
		return f.start(ctx, sp, true)
	}
	if label == "" {
		return Started{}, fmt.Errorf("%s: %w", name, ErrNoLabel)
	}
	if !f.ForeignRepo && !Belongs(f.P.Name, bead) {
		return Started{}, fmt.Errorf("%s is not a %s bead (want %s-<n>[.<m>]): %w (or pass --repo)", bead, f.P.Name, f.P.Name, ErrWrongRepo)
	}
	b, err := f.B.Show(ctx, bead)
	if err != nil {
		return Started{}, err
	}
	wss, err := f.H.Workspaces(ctx)
	if err != nil {
		return Started{}, err
	}
	// Find an orphan by its worktree, never its label: the path is built forward from
	// the name, while a label is free text.
	wt := filepath.Join(f.P.Repo+"-WT", name)
	if i := slices.IndexFunc(wss, func(w Workspace) bool { return w.Path != "" && filepath.Clean(w.Path) == wt }); i >= 0 {
		return Started{}, fmt.Errorf("%s: %w; check it, then remove it with: herdr worktree remove --workspace %s --force", name, ErrOrphan, wss[i].ID)
	}

	ws, err := f.H.CreateWorktree(ctx, f.P.Repo, name, wt, label)
	if err != nil {
		return Started{}, err
	}
	// Record straight away: any failure after this point is healed by spawning again.
	sp = logbook.Spawn{Name: name, Bead: bead, Label: label, Workspace: ws.ID, Pane: ws.Pane, Worktree: ws.Path, Branch: name, Repo: f.P.Repo}
	if sp.ID, err = f.LB.RecordSpawn(ctx, f.Me, sp, brief(b)); err != nil {
		return Started{}, err
	}
	return f.start(ctx, sp, false)
}

// beeSettings is passed with --settings: Claude reads settings.local.json from the main
// checkout even in a worktree, so per-bee settings can't live in a file. The bee's bs
// finds its name from its Claude process and its project from the worktree's git dir.
const beeSettings = `{"crossSessionInbound":"accept"}`

// start starts Claude with a doorbell as its first prompt, so the bee reads its unread
// messages without needing a ring. A resumed bee continues its last conversation in
// the worktree instead of starting its task over, if it has one: a bee that never read
// its inbox since this spawn (its first start failed) starts fresh, since
// claude --continue exits when there is no conversation.
func (f Fleet) start(ctx context.Context, sp logbook.Spawn, resume bool) (Started, error) {
	// In another repo (--repo, now or when first spawned) the bee's own bs resolves that
	// repo's project: a system prompt naming P would mislead it, and its receipts go to
	// that project's logbook, so sp.Read can't tell and a restart always continues.
	foreign := f.ForeignRepo || cmp.Or(sp.Repo, f.P.Repo) != f.P.Repo
	st := Started{Name: sp.Name, Label: sp.Label, Workspace: sp.Workspace, Resumed: resume, Continued: resume && (sp.Read || foreign)}
	doorbell := fmt.Sprintf("[bs] you are %s, working on %s — run: bs inbox   (reply with: bs send %s \"...\")", sp.Name, sp.Bead, f.Me)
	args := []string{"--name", sp.Name, "--settings", beeSettings}
	if !foreign {
		file, err := prompt.File(prompt.Worker, f.P.Name)
		if err != nil {
			return st, err
		}
		args = append(args, "--append-system-prompt-file", file)
	}
	args = append(args, doorbell)
	if st.Continued {
		args = append([]string{"--continue"}, args...)
	}
	err := f.H.StartClaude(ctx, sp.Pane, sp.Name, args...)
	switch {
	case errors.Is(err, ErrWaiting):
		st.Waiting, err = true, nil
	case errors.Is(err, ErrUnconfirmed):
		// A bee goes straight to work on its doorbell, so herdr, which waits for an idle
		// prompt, usually times out on a healthy bee. Ask Claude's registry instead.
		alive, aerr := f.S.Alive(ctx, sp.Name, confirmWait)
		switch {
		case aerr != nil:
			err = aerr
		case alive:
			err = nil
		default:
			err = fmt.Errorf("%w: no Claude session named %s appeared within %s; read its pane (herdr pane read %s), then spawn again", err, sp.Name, confirmWait, sp.Pane)
		}
	}
	return st, err
}

// confirmWait is how long start waits for Claude's registry after herdr gives up.
// Claude registers about 3 s after launch (herdr 0.9.3, bees-1.14).
const confirmWait = 20 * time.Second

// End removes the bee's recorded workspace and records the end. It never asks herdr
// which workspace to remove. Ending an ended bee is a no-op. The bee's branch is
// deleted once its work is proven merged, so the bead can be spawned again; kept says
// why a branch was left in place with its work ("" when deleted or absent).
func (f Fleet) End(ctx context.Context, name string) (kept string, err error) {
	sp, ok, err := f.LB.LatestSpawn(ctx, name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%s: %w", name, ErrNotSpawned)
	}
	if sp.Ended {
		return "", nil
	}
	if err := f.H.RemoveWorktree(ctx, sp.Workspace); err != nil {
		return "", err
	}
	repo := cmp.Or(sp.Repo, f.P.Repo)
	if exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+sp.Branch).Run() == nil {
		kept = dropBranch(ctx, f.G, repo, sp.Branch)
	}
	return kept, f.LB.RecordEnd(ctx, f.Me, sp.ID)
}

func brief(b Bead) string {
	return fmt.Sprintf("You own %s: %s\n\n%s\n\nRun `bd show %s` for the full bead.", b.ID, b.Title, b.Description, b.ID)
}
