// Package worker starts and ends workers: one Claude Code session per bead, each in its
// own herdr workspace and git worktree (ADR-0006).
package worker

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/project"
	"github.com/jmmshn/bees/internal/prompt"
)

var (
	ErrRunning = errors.New("worker is already running")
	ErrOrphan  = errors.New("a herdr workspace is open on this worker's worktree but bs has no record of it")
	ErrWaiting = errors.New("claude is waiting at a prompt") // started, but needs an answer in its pane
	// ErrUnconfirmed: herdr stopped waiting for Claude to be ready. Claude may be busy
	// with its first prompt or may have exited; only Claude's registry can tell.
	ErrUnconfirmed = errors.New("herdr could not confirm that claude started")
	ErrWrongRepo   = errors.New("bead does not belong to this project")
	ErrNotSpawned  = errors.New("bs has no record of spawning this worker")
	ErrNoLabel     = errors.New("a first spawn needs a label")
	ErrBadLabel    = errors.New("not a valid label")
	ErrLabelChange = errors.New("the worker is recorded with another label")
	ErrTwice       = errors.New("named twice in one spawn")
	ErrNotStarted  = errors.New("not started: another worker in this spawn was refused")
	ErrDirty       = errors.New("the worktree has modified or untracked files")
	ErrNotOurs     = errors.New("the recorded workspace is open on another worktree")
	// ErrHerdrDown: herdr didn't answer, so bs can't tell an open workspace from a gone one.
	ErrHerdrDown = errors.New("herdr is unreachable")
)

// LabelRules says what a worker's label must be. The queen writes the label; bs only checks it.
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
	// RemoveWorktree removes workspace and its worktree; already gone → nil. Without force
	// it refuses a worktree with modified or untracked files: ErrDirty. Both: ErrHerdrDown
	// when herdr itself didn't answer.
	RemoveWorktree(ctx context.Context, workspace string, force bool) error
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

// Fleet spawns and ends the workers of one project.
type Fleet struct {
	P           project.Project
	LB          *logbook.Logbook
	H           Herdr
	S           Sessions
	B           Beads
	G           GitHub // nil: End proves a merge from git alone
	Me          string // the queen doing the spawning
	ForeignRepo bool   // skip the check that the bead belongs to P (--repo)
	Model       string // claude --model for every start; "" leaves Claude's own default
}

var herdrName = project.SessionName

// Name is the worker's one name for a bead: session, herdr agent, branch, worktree dir
// and address. Bead ids are Jira-style, <project>-<n>[.<m>…] (R27), and project names
// have no "-", so the name is the id with "." → "-": bees-12.3 → bees-12-3.
func Name(bead string) (string, error) {
	n := strings.ReplaceAll(bead, ".", "-")
	if !herdrName.MatchString(n) {
		return "", fmt.Errorf("%q can't be a worker name (lowercase, starting with a letter, at most 32 characters)", bead)
	}
	return n, nil
}

var beadNumber = regexp.MustCompile(`^-\d+(\.\d+)*$`)

// Belongs reports whether bead is one of project's: <project>-<n>[.<m>…].
func Belongs(project, bead string) bool {
	rest, ok := strings.CutPrefix(bead, project)
	return ok && beadNumber.MatchString(rest)
}

var workerNumber = regexp.MustCompile(`^(-\d+)+$`)

// IsWorkerOf reports whether name is the worker name of one of project's beads:
// <project>-<n>[-<m>…]. A bare prefix test would also take a session named bees-review.
func IsWorkerOf(project, name string) bool {
	rest, ok := strings.CutPrefix(name, project)
	return ok && workerNumber.MatchString(rest)
}

// Address maps to, a worker's name or a bead id of project, forward to the worker's name:
// bs end and send take either spelling (bees-6.1). A bead id whose worker was never spawned
// is refused, so nothing queues for a session that can't exist.
func Address(ctx context.Context, lb *logbook.Logbook, project, to string) (string, error) {
	if !Belongs(project, to) || !strings.Contains(to, ".") {
		return to, nil
	}
	name, err := Name(to)
	if err != nil {
		return "", err
	}
	if _, ok, err := lb.LatestSpawn(ctx, name); err != nil || ok {
		return name, err
	}
	return "", fmt.Errorf("%s is a bead id, whose worker is %s: %w", to, name, ErrNotSpawned)
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

// Started describes a worker Spawn started.
type Started struct {
	Name, Label, Workspace string
	Resumed                bool // restarted in its recorded workspace
	Continued              bool // resumed with its last conversation (claude --continue)
	Waiting                bool // Claude is waiting at a prompt (e.g. folder trust) in its pane
}

// Spawn starts a worker for bead with label as its herdr workspace label, or restarts it
// in its recorded workspace if its session has died. A restart keeps the recorded
// label: label may be empty, and any other label is refused. Respawning an ended
// worker with an empty label reuses its recorded one.
func (f Fleet) Spawn(ctx context.Context, bead, label string) (Started, error) {
	p, err := f.check(ctx, bead, label)
	if err != nil {
		return Started{}, err
	}
	sp, err := f.open(ctx, p)
	if err != nil {
		return Started{}, err
	}
	return f.start(ctx, sp, p.resume)
}

// Request asks SpawnAll for one worker.
type Request struct{ Bead, Label string }

// Result is what SpawnAll did for one Request.
type Result struct {
	Bead    string
	Started Started
	Err     error
	Took    time.Duration
}

// Refused reports whether err is a mistake in the spawn request itself (a bad,
// missing or changed label, a worker named twice): fix the command and run it again.
func Refused(err error) bool {
	return errors.Is(err, ErrBadLabel) || errors.Is(err, ErrNoLabel) || errors.Is(err, ErrLabelChange) || errors.Is(err, ErrTwice)
}

// SpawnAll spawns a worker for each request, as Spawn does, and returns one Result per
// request in order. It checks every request before touching anything: if any is
// Refused, none starts, and the others fail with ErrNotStarted. Then each worker's
// worktree and spawn record are made one worker at a time (git and herdr state stay
// simple), and the slow part, waiting for each Claude to start, runs concurrently.
// Past the checks, one failure doesn't stop the others. A worker named twice is refused
// the second time: two starts of one worker would race into two Claudes.
func (f Fleet) SpawnAll(ctx context.Context, reqs []Request) []Result {
	out := make([]Result, len(reqs))
	plans := make([]plan, len(reqs))
	seen := map[string]bool{}
	refused := false
	for i, r := range reqs {
		t0 := time.Now()
		out[i].Bead = r.Bead
		if name, err := Name(r.Bead); err == nil && seen[name] {
			out[i].Err = fmt.Errorf("%s: %w", name, ErrTwice)
		} else {
			plans[i], out[i].Err = f.check(ctx, r.Bead, r.Label)
			seen[plans[i].name] = out[i].Err == nil // a refused first try doesn't block a corrected repeat
		}
		out[i].Took = time.Since(t0)
		refused = refused || Refused(out[i].Err)
	}
	if refused {
		for i := range out {
			if out[i].Err == nil {
				out[i].Err = ErrNotStarted
			}
		}
		return out
	}
	var wg sync.WaitGroup
	for i, p := range plans {
		if out[i].Err != nil {
			continue
		}
		t0 := time.Now()
		sp, err := f.open(ctx, p)
		if err != nil {
			out[i].Err, out[i].Took = err, out[i].Took+time.Since(t0)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i].Started, out[i].Err = f.start(ctx, sp, p.resume)
			out[i].Took += time.Since(t0)
		}()
	}
	wg.Wait()
	return out
}

// plan is a checked spawn; nothing has been touched yet.
type plan struct {
	name, bead, label string
	sp                logbook.Spawn // resume: the recorded spawn
	resume            bool          // the worker is recorded and its session has died
	b                 Bead          // a first spawn's bead, for its brief
}

// check decides whether bead can be spawned, without touching herdr or the logbook.
func (f Fleet) check(ctx context.Context, bead, label string) (plan, error) {
	name, err := Name(bead)
	if err != nil {
		return plan{}, err
	}
	p := plan{name: name, bead: bead, label: label}
	// Check the launch line before anything is made, so a launch too long to type leaves
	// no worktree or record behind; the settings file's * is at most 10 digits.
	dir, err := prompt.Dir()
	if err != nil {
		return p, err
	}
	worst := filepath.Join(dir, strings.Replace(settingsPattern(name), "*", "4294967295", 1))
	if err := CheckAgentArgs(f.launch(name, bead, worst, filepath.Join(dir, f.P.Name+"-worker.md"), true)); err != nil {
		return p, fmt.Errorf("%s: %w", name, err)
	}
	if label != "" {
		if err := CheckLabel(label); err != nil {
			return p, err
		}
	}
	sp, ok, err := f.LB.LatestSpawn(ctx, name)
	if err != nil {
		return p, err
	}
	if ok && !sp.Ended {
		if label != "" && label != sp.Label {
			return p, fmt.Errorf("%s is labelled %q, not %q: %w; spawn it again without --label", name, sp.Label, label, ErrLabelChange)
		}
		// Fail closed: an unreadable registry must not start a second Claude.
		if alive, err := f.S.Alive(ctx, name, 0); err != nil {
			return p, err
		} else if alive {
			return p, fmt.Errorf("%s: %w", name, ErrRunning)
		}
		p.sp, p.resume = sp, true
		_, err := f.P.LogbookDir() // start needs it; refuse before anything is touched
		return p, err
	}
	if label == "" && ok {
		p.label = sp.Label // an ended worker comes back under its recorded label
	}
	if p.label == "" {
		return p, fmt.Errorf("%s: %w", name, ErrNoLabel)
	}
	if !f.ForeignRepo && !Belongs(f.P.Name, bead) {
		return p, fmt.Errorf("%s is not a %s bead (want %s-<n>[.<m>]): %w (or pass --repo)", bead, f.P.Name, f.P.Name, ErrWrongRepo)
	}
	if p.b, err = f.B.Show(ctx, bead); err != nil {
		return p, err
	}
	wss, err := f.H.Workspaces(ctx)
	if err != nil {
		return p, err
	}
	// Find an orphan by its worktree, never its label: the path is built forward from
	// the name, while a label is free text.
	wt := f.worktree(name)
	if i := slices.IndexFunc(wss, func(w Workspace) bool { return w.Path != "" && filepath.Clean(w.Path) == wt }); i >= 0 {
		return p, fmt.Errorf("%s: %w; check it, then remove it with: herdr worktree remove --workspace %s --force", name, ErrOrphan, wss[i].ID)
	}
	_, err = f.P.LogbookDir()
	return p, err
}

func (f Fleet) worktree(name string) string { return filepath.Join(f.P.Repo+"-WT", name) }

// open opens a first spawn's worktree and records the spawn; a resume has both already.
func (f Fleet) open(ctx context.Context, p plan) (logbook.Spawn, error) {
	if p.resume {
		return p.sp, nil
	}
	ws, err := f.H.CreateWorktree(ctx, f.P.Repo, p.name, f.worktree(p.name), p.label)
	if err != nil {
		return logbook.Spawn{}, err
	}
	// Record straight away: any failure after this point is healed by spawning again.
	sp := logbook.Spawn{Name: p.name, Bead: p.bead, Label: p.label, Workspace: ws.ID, Pane: ws.Pane, Worktree: ws.Path, Branch: p.name, Repo: f.P.Repo}
	sp.ID, err = f.LB.RecordSpawn(ctx, f.Me, sp, brief(p.b))
	return sp, err
}

// workerSettings is passed with --settings, as a private file in bs's cache made fresh at
// each start (inline JSON is too long to type into a pane: bees-8.3) and removed by bs end
// or a failed start. It stays while the worker runs, since Claude may read its settings
// files again after startup. Claude reads settings.local.json from the main
// checkout even in a worktree, so per-worker settings can't live in the repo, and the auto
// mode classifier reads autoMode only from user, managed or --settings settings. The
// worker's bs finds its name from its Claude process and its project from env.BS_PROJECT,
// the spawner's project: never from the worktree's git dir, which names another project in
// a --repo worktree and would send its messages and receipts to that logbook (bees-1.54).
// Its logbook is env.BS_LOGBOOK_DIR, the spawner's, with any BS_LOGBOOK the pane inherits
// from herdr blanked: else a queen and her workers could split the store (bees-11.3).
//
// A worker owns its worktree and branch, so the classifier may let it build, test and push
// its own branch (bees-1.51). The environment says the keeper merges by hand only when
// GitHub reports the default branch protected (protected): unchecked, that claim would
// relax the classifier on pushes (bees-7.4). Every autoMode list keeps "$defaults": a list
// without it replaces the built-in rules (force push, exfiltration).
//
// The deny rules run before the classifier. A rule's * matches any text, a space or a
// slash too (probed on Claude Code 2.1.302), so git -* push covers git -C dir push and
// git -c k=v push, and */git push covers /usr/bin/git push. They are a speed bump, not
// the guard: a shell finds other spellings, and branch protection on main is the real
// one (docs/SETUP.md). Not git * push: that would deny a commit whose message says
// "push … main".
func workerSettings(project, logbookDir string, sp logbook.Spawn, protected bool) (string, error) {
	branch := cmp.Or(sp.Branch, sp.Name)
	var deny []string
	for _, git := range []string{"git", "command git", "*/git"} {
		for _, push := range []string{git + " push", git + " -* push"} {
			for _, bad := range []string{"--force*", "-f*", "+*", "main*", "master*", "*:main*", "*:master*", "*refs/heads/main*", "*refs/heads/master*"} {
				deny = append(deny, "Bash("+push+" "+bad+")")
				if !strings.HasPrefix(bad, "*") { // a leading * already covers the words before it
					deny = append(deny, "Bash("+push+" * "+bad+")")
				}
			}
		}
	}
	deny = append(deny, "Bash(gh pr merge*)", "Bash(gh * pr merge*)", "Bash(gh pr review* --approve*)", "Bash(gh pr review* -a*)", "Bash(gh api *merge*)")
	env := fmt.Sprintf("Host containment: a bees worker, working in its own git worktree %s on its own branch %s of the repository %s.", sp.Worktree, branch, sp.Repo)
	if protected {
		env += " GitHub protects the repository's default branch, so the worker's work reaches it only through a pull request the keeper merges."
	}
	b, err := json.Marshal(map[string]any{
		"crossSessionInbound": "accept",
		"env":                 map[string]string{"BS_PROJECT": project, "BS_LOGBOOK_DIR": logbookDir, "BS_LOGBOOK": ""},
		"autoMode": map[string][]string{
			"environment": {"$defaults", env},
			"allow": {"$defaults",
				fmt.Sprintf("Running this repository's own build, lint and test commands (such as go vet, go test, go build -o into the worktree, pre-commit) inside the worktree %s.", sp.Worktree),
				fmt.Sprintf("Committing to and pushing (never force-pushing) the worker's own branch %s to the repository's origin, opening a pull request from it, and pushing further commits to that open pull request.", branch),
				"Running bs and bd, the bees tools, to read and send its project's messages and to read and update its own beads.",
			},
		},
		"permissions": map[string][]string{"deny": deny},
	})
	return string(b), err
}

// start starts Claude with a doorbell as its first prompt, so the worker reads its unread
// messages without needing a ring. A resumed worker continues its last conversation in
// the worktree instead of starting its task over, if it has one: a worker that never read
// its inbox since this spawn (its first start failed) starts fresh, since
// claude --continue exits when there is no conversation.
func (f Fleet) start(ctx context.Context, sp logbook.Spawn, resume bool) (Started, error) {
	st := Started{Name: sp.Name, Label: sp.Label, Workspace: sp.Workspace, Resumed: resume, Continued: resume && sp.Read}
	lbDir, err := f.P.LogbookDir()
	if err != nil {
		return st, err
	}
	settings, err := workerSettings(f.P.Name, lbDir, sp, f.protected(ctx, sp))
	if err != nil {
		return st, err
	}
	// Only a worker that isn't running gets here, so earlier starts' files are spent.
	if err := prompt.Remove(sp.Name + "-settings*.json"); err != nil {
		return st, err
	}
	settingsFile, err := prompt.Private(settingsPattern(sp.Name), settings)
	if err != nil {
		return st, err
	}
	file, err := prompt.File(prompt.Worker, f.P.Name)
	if err != nil {
		os.Remove(settingsFile)
		return st, err
	}
	err = f.H.StartClaude(ctx, sp.Pane, sp.Name, f.launch(sp.Name, sp.Bead, settingsFile, file, st.Continued)...)
	switch {
	case errors.Is(err, ErrWaiting):
		st.Waiting, err = true, nil
	case errors.Is(err, ErrUnconfirmed):
		// A worker goes straight to work on its doorbell, so herdr, which waits for an idle
		// prompt, usually times out on a healthy worker. Ask Claude's registry instead.
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
	if err != nil {
		os.Remove(settingsFile)
	}
	return st, err
}

// settingsPattern names a worker's settings files, for prompt.Private.
func settingsPattern(name string) string { return name + "-settings-*.json" }

// launch is the claude command line that starts worker name, with a doorbell as its first
// prompt so it reads its unread messages without needing a ring.
func (f Fleet) launch(name, bead, settings, promptFile string, cont bool) []string {
	doorbell := fmt.Sprintf("[bs] you are %s, working on %s — run: bs inbox   (reply with: bs send %s \"...\")", name, bead, f.Me)
	args := []string{"--name", name, "--settings", settings, "--append-system-prompt-file", promptFile, doorbell}
	if f.Model != "" {
		// On the line, not in --settings: --model beats ANTHROPIC_MODEL and settings files.
		// One argument, so a value starting with - can't become a flag of its own.
		args = append([]string{"--model=" + f.Model}, args...)
	}
	if cont {
		args = append([]string{"--continue"}, args...)
	}
	return args
}

// protected reports whether GitHub protects the default branch of sp's repo. Without
// GitHub, or when gh fails, it says no: the worker is then told nothing about merges.
func (f Fleet) protected(ctx context.Context, sp logbook.Spawn) bool {
	if f.G == nil {
		return false
	}
	ok, err := f.G.Protected(ctx, cmp.Or(sp.Repo, f.P.Repo))
	return err == nil && ok
}

// confirmWait is how long start waits for Claude's registry after herdr gives up.
// Claude registers about 3 s after launch (herdr 0.9.3, bees-1.14).
const confirmWait = 20 * time.Second

// EndOpts says how far ending a worker may go.
type EndOpts struct {
	Force bool // remove a worktree with uncommitted or untracked files, discarding them
	// Forget records the end of a worker whose recorded workspace herdr has handed to
	// another worktree, leaving that workspace open: without it ErrNotOurs is a dead end.
	Forget bool
}

// End removes the worker's recorded workspace and records the end. It never asks herdr
// which workspace to remove, and removes the recorded one only while it is still open on
// the recorded worktree, so a reused workspace id can't take another worktree with it
// (bees-7.4). A worktree with uncommitted or untracked files is kept unless o.Force: ErrDirty.
// Ending an ended worker is a no-op. The worker's branch is always kept, with its
// committed work: Prune deletes it once merged.
func (f Fleet) End(ctx context.Context, name string, o EndOpts) error {
	sp, ok, err := f.LB.LatestSpawn(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: %w", name, ErrNotSpawned)
	}
	if sp.Ended {
		return nil
	}
	e := f.end(ctx, sp, o)
	return cmp.Or(e.Err, e.Warn)
}

// Ending is what EndAll did with one worker.
type Ending struct {
	Name, Workspace string
	Gone            bool  // herdr no longer had the workspace, so only the end was recorded
	Forgot          bool  // the workspace is open on another worktree: the end was recorded, the workspace left open
	Err             error // the worker was kept: still live in the logbook
	Warn            error // the worker was ended, but removing its settings files failed
}

// EndAll ends every worker the logbook records as spawned and not yet ended, and nothing
// else: never a workspace guessed from herdr (motif 12). That includes a worker spawned with
// --repo for another project's bead (dotfiles-3): this project's queen recorded it. A
// worker it can't end stays live in the logbook, so running it again picks up where it
// stopped.
func (f Fleet) EndAll(ctx context.Context, o EndOpts) ([]Ending, error) {
	live, err := f.LB.LiveSpawns(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Ending, len(live))
	for i, sp := range live {
		out[i] = f.end(ctx, sp, o)
	}
	return out, nil
}

// end removes live spawn sp's workspace and records the end.
func (f Fleet) end(ctx context.Context, sp logbook.Spawn, o EndOpts) Ending {
	e := Ending{Name: sp.Name, Workspace: sp.Workspace}
	wss, err := f.H.Workspaces(ctx)
	if err != nil {
		e.Err = fmt.Errorf("%s: can't tell whether workspace %s is open; nothing removed: %w", sp.Name, sp.Workspace, err)
		return e
	}
	i := slices.IndexFunc(wss, func(w Workspace) bool { return w.ID == sp.Workspace })
	e.Gone = i < 0
	switch {
	case e.Gone:
	case sp.Worktree == "" || filepath.Clean(wss[i].Path) != filepath.Clean(sp.Worktree):
		if !o.Forget {
			e.Err = fmt.Errorf("%s: workspace %s is open on %q, not its recorded worktree %q: %w; nothing removed (--forget records the end and leaves it open)", sp.Name, sp.Workspace, wss[i].Path, sp.Worktree, ErrNotOurs)
			return e
		}
		e.Forgot = true
	default:
		if err := f.H.RemoveWorktree(ctx, sp.Workspace, o.Force); err != nil {
			if errors.Is(err, ErrDirty) {
				err = fmt.Errorf("%w in %s; commit or move them, or end it with --force to discard them", ErrDirty, sp.Worktree)
			}
			e.Err = fmt.Errorf("%s: %w", sp.Name, err)
			return e
		}
	}
	if err := f.LB.RecordEnd(ctx, f.Me, sp.ID); err != nil {
		e.Err = err
		return e
	}
	e.Warn = prompt.Remove(sp.Name + "-settings*.json") // * also takes the fixed name of 8.3's first cut
	return e
}

// Pruned is what Prune found for one ended worker's branch.
type Pruned struct {
	Branch, Repo string
	Merge        Merge
	Why          string // how the work was proven merged, or why the branch was kept
	InUse        string // the worktree that has the branch in use; kept, whatever Merge says
	Prunable     bool   // InUse's directory is gone: git worktree prune frees the branch
	Deleted      bool
	DeleteErr    error // proven merged, but git branch -D failed
}

// Prune deletes the branch of each ended worker once its work is proven merged; dry only
// reports. It looks only at branches the logbook recorded (never guessed from git),
// skips branches already gone, keeps any branch a worktree has checked out (someone may
// have taken it over: bees-8.4), and never touches a remote.
func (f Fleet) Prune(ctx context.Context, dry bool) ([]Pruned, error) {
	ended, err := f.LB.EndedSpawns(ctx)
	if err != nil {
		return nil, err
	}
	var out []Pruned
	for _, sp := range ended {
		repo := cmp.Or(sp.Repo, f.P.Repo)
		if gitRun(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+sp.Branch) != nil {
			continue
		}
		p := Pruned{Branch: sp.Branch, Repo: repo}
		p.Merge, p.Why = merged(ctx, f.G, repo, sp.Branch)
		if p.InUse, p.Prunable, err = checkedOut(ctx, repo, sp.Branch); err != nil {
			p.Merge, p.Why = Unknown, err.Error()
		}
		if p.Merge == Merged && p.InUse == "" && !dry {
			p.DeleteErr = gitRun(ctx, repo, "branch", "-D", sp.Branch)
			p.Deleted = p.DeleteErr == nil
			// git knows in-use states bs doesn't look for; its refusal names the worktree.
			if _, at, ok := strings.Cut(fmt.Sprint(p.DeleteErr), "used by worktree at "); ok {
				p.InUse, p.DeleteErr = strings.Trim(strings.TrimSpace(at), "'"), nil
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func brief(b Bead) string {
	return fmt.Sprintf("You own %s: %s\n\n%s\n\nRun `bd show %s` for the full bead.", b.ID, b.Title, b.Description, b.ID)
}
