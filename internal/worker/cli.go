package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jmmshn/bees/internal/harness/claude"
)

// HerdrCLI drives the herdr command line. Every herdr command prints JSON.
type HerdrCLI struct{}

// herdrError is herdr's error envelope, printed with a non-zero exit.
type herdrError struct {
	Error struct{ Code, Message string } `json:"error"`
}

func herdr(ctx context.Context, out any, args ...string) (code string, err error) {
	b, err := exec.CommandContext(ctx, "herdr", args...).Output()
	if err != nil {
		var he herdrError
		var ee *exec.ExitError
		if errors.As(err, &ee) && json.Unmarshal(ee.Stderr, &he) == nil && he.Error.Code != "" {
			return he.Error.Code, fmt.Errorf("herdr %s: %s", args[0], he.Error.Message)
		}
		if json.Unmarshal(b, &he) == nil && he.Error.Code != "" {
			return he.Error.Code, fmt.Errorf("herdr %s: %s", args[0], he.Error.Message)
		}
		return "", fmt.Errorf("herdr %v: %w", args, err)
	}
	if out == nil {
		return "", nil
	}
	if err := json.Unmarshal(bytes.TrimSpace(b), out); err != nil {
		return "", fmt.Errorf("herdr %v: parse output: %w", args, err)
	}
	return "", nil
}

func (HerdrCLI) CreateWorktree(ctx context.Context, repo, branch, path, label string) (Workspace, error) {
	var r struct {
		Result struct {
			Workspace struct {
				ID string `json:"workspace_id"`
			} `json:"workspace"`
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
			Worktree struct {
				Path string `json:"path"`
			} `json:"worktree"`
		} `json:"result"`
	}
	if _, err := herdr(ctx, &r, "worktree", "create", "--cwd", repo, "--branch", branch, "--path", path, "--label", label, "--no-focus"); err != nil {
		return Workspace{}, err
	}
	if r.Result.Workspace.ID == "" || r.Result.RootPane.ID == "" {
		return Workspace{}, fmt.Errorf("herdr worktree create: no workspace or pane id in its output")
	}
	return Workspace{ID: r.Result.Workspace.ID, Label: label, Pane: r.Result.RootPane.ID, Path: r.Result.Worktree.Path}, nil
}

// ErrUnsafeArg is an agent argument herdr refuses to pass to the agent's shell.
var ErrUnsafeArg = errors.New("herdr can't pass an agent argument containing a control character")

// CheckAgentArgs applies herdr's rule for agent arguments: herdr agent start refuses
// any argument containing a Unicode control character (newline, tab, ESC, DEL, C1) with
// invalid_agent_argument (probed on herdr 0.9.3; TestCheckAgentArgsMatchesRealHerdr
// re-checks the installed herdr). Text that may span lines goes in a file instead.
func CheckAgentArgs(args []string) error {
	for i, a := range args {
		if strings.IndexFunc(a, unicode.IsControl) >= 0 {
			return fmt.Errorf("agent argument %d (%.40q): %w", i, a, ErrUnsafeArg)
		}
	}
	return nil
}

func (HerdrCLI) StartClaude(ctx context.Context, pane, name string, args ...string) error {
	if err := CheckAgentArgs(args); err != nil {
		return err
	}
	code, err := herdr(ctx, nil, append([]string{"agent", "start", name, "--kind", "claude", "--pane", pane, "--timeout", strconv.FormatInt(herdrStartTimeout.Milliseconds(), 10), "--"}, args...)...)
	// Codes seen with herdr 0.9.3 (bees-1.14). agent_not_ready: blocked during startup,
	// e.g. at Claude's folder-trust prompt, reported about 3 s in. timeout: Claude never
	// sat idle at its prompt in time, either busy with its first prompt or exited (as
	// claude --continue does with no conversation); herdr then leaves the agent unnamed.
	switch code {
	case "agent_not_ready":
		return fmt.Errorf("%w: %w", err, ErrWaiting)
	case "timeout":
		return fmt.Errorf("%w: %w", err, ErrUnconfirmed)
	}
	return err
}

// herdrStartTimeout bounds herdr's wait for an idle Claude: long enough for a cold
// start (3-6 s) and the trust prompt (3.4 s), short of herdr's 30 s default, because a
// worker busy with its doorbell is never idle in time.
const herdrStartTimeout = 10 * time.Second

func (HerdrCLI) RemoveWorktree(ctx context.Context, workspace string) error {
	code, err := herdr(ctx, nil, "worktree", "remove", "--workspace", workspace, "--force")
	if code == "workspace_not_found" {
		return nil
	}
	return err
}

func (HerdrCLI) Workspaces(ctx context.Context) ([]Workspace, error) {
	var r struct {
		Result struct {
			Workspaces []struct {
				ID       string `json:"workspace_id"`
				Label    string `json:"label"`
				Worktree *struct {
					Path string `json:"checkout_path"`
				} `json:"worktree"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if _, err := herdr(ctx, &r, "workspace", "list"); err != nil {
		return nil, err
	}
	var out []Workspace
	for _, w := range r.Result.Workspaces {
		ws := Workspace{ID: w.ID, Label: w.Label}
		if w.Worktree != nil {
			ws.Path = w.Worktree.Path
		}
		out = append(out, ws)
	}
	return out, nil
}

// ClaudeSessions finds sessions in Claude's registry.
type ClaudeSessions struct{}

func (ClaudeSessions) Alive(ctx context.Context, name string, wait time.Duration) (bool, error) {
	deadline := time.Now().Add(wait) // wait 0: one look
	for {
		_, err := claude.Find(name)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, claude.ErrNotRunning) {
			return false, err
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		// ponytail: polls the registry; watching ~/.claude/sessions would wake sooner.
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// BeadsCLI reads beads with the bd command line.
type BeadsCLI struct{}

func (BeadsCLI) Show(ctx context.Context, id string) (Bead, error) {
	out, err := exec.CommandContext(ctx, "bd", "show", id, "--json").Output()
	if err != nil {
		return Bead{}, fmt.Errorf("bd show %s: %w", id, err)
	}
	var bs []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &bs); err != nil {
		return Bead{}, fmt.Errorf("bd show %s: %w", id, err)
	}
	if len(bs) == 0 {
		return Bead{}, fmt.Errorf("bd show %s: no such bead", id)
	}
	b := bs[0]
	return Bead{ID: b.ID, Title: b.Title, Description: b.Description}, nil
}

// IDs lists every bead id in the store, open or closed.
func (BeadsCLI) IDs(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "bd", "list", "--all", "--limit", "0", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("bd list: %w", err)
	}
	var bs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &bs); err != nil {
		return nil, fmt.Errorf("bd list: %w", err)
	}
	ids := make([]string, len(bs))
	for i, b := range bs {
		ids[i] = b.ID
	}
	return ids, nil
}

// Create files a bead under parent and returns its id; bd numbers it <parent>.<n>.
// --force because Jira-style ids don't use the store's own prefix. Empty optional
// fields keep bd's defaults.
func (BeadsCLI) Create(ctx context.Context, parent, title, description, typ, priority string) (string, error) {
	args := []string{"create", "--silent", "--force", "--parent", parent, "--title", title}
	for _, f := range [][2]string{{"--description", description}, {"--type", typ}, {"--priority", priority}} {
		if f[1] != "" {
			args = append(args, f[0], f[1])
		}
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Stderr = &stderr // kept out of stdout, which is only the id
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("bd create under %s: %w: %s", parent, err, bytes.TrimSpace(append(out, stderr.Bytes()...)))
	}
	return string(bytes.TrimSpace(out)), nil
}

// CreateEpic creates an epic with an explicit id. --force because Jira-style ids don't
// use the store's own prefix.
func (BeadsCLI) CreateEpic(ctx context.Context, id, title string) error {
	out, err := exec.CommandContext(ctx, "bd", "create", "--silent", "--force", "--type", "epic", "--id", id, "--title", title).CombinedOutput()
	if err != nil {
		return fmt.Errorf("bd create %s: %w: %s", id, err, bytes.TrimSpace(out))
	}
	return nil
}

// GitHubCLI asks GitHub through the gh command line.
type GitHubCLI struct{}

// ghTimeout bounds a gh call: bs end must not hang on a slow or offline network.
const ghTimeout = 15 * time.Second

func (GitHubCLI) MergedHeads(ctx context.Context, repo, branch string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--head", branch, "--state", "merged", "--json", "headRefOid", "--limit", "100")
	cmd.Dir, cmd.Stderr, cmd.WaitDelay = repo, &stderr, time.Second
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("gh pr list: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	var prs []struct {
		Head string `json:"headRefOid"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	heads := make([]string, len(prs))
	for i, p := range prs {
		heads[i] = p.Head
	}
	return heads, nil
}

// Protected asks GitHub whether the default branch is protected: a branch's protected
// field is readable with read access, while its protection rules need admin.
func (GitHubCLI) Protected(ctx context.Context, repo string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	gh := func(args ...string) (string, error) {
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, "gh", args...)
		cmd.Dir, cmd.Stderr, cmd.WaitDelay = repo, &stderr, time.Second
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("gh %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(string(out)), nil
	}
	def, err := gh("api", "repos/{owner}/{repo}", "--jq", ".default_branch")
	if err != nil {
		return false, err
	}
	prot, err := gh("api", "repos/{owner}/{repo}/branches/"+def, "--jq", ".protected")
	return prot == "true", err
}
