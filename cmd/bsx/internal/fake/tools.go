package fake

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// Commands routes each argv to its fake by program, and every other program (git) to
// Real: real git in temp dirs (ADR-0010 Tests).
type Commands struct {
	Herdr *Herdr
	GH    *GH
	CL    *CL
	Real  port.Runner
}

func (c Commands) Run(ctx context.Context, dir string, argv ...string) ([]byte, error) {
	if _, err := os.Stat(dir); dir != "" && err != nil && len(argv) > 0 {
		// A real program can't start in a cwd that is gone: neither can a fake one (motif 13).
		return nil, fmt.Errorf("%s: can't run in %s: %w", argv[0], dir, err)
	}
	switch {
	case len(argv) == 0:
		return nil, fmt.Errorf("fake: empty argv")
	case argv[0] == "herdr" && c.Herdr != nil:
		return c.Herdr.Run(ctx, dir, argv...)
	case argv[0] == "gh" && c.GH != nil:
		return c.GH.Run(ctx, dir, argv...)
	case argv[0] == "cl" && c.CL != nil:
		return c.CL.Run(ctx, dir, argv...)
	case c.Real != nil:
		return c.Real.Run(ctx, dir, argv...)
	}
	return nil, fmt.Errorf("fake: no runner for %s", argv[0])
}

// PR is a pull request as the fake gh holds it. With GH.Git set, its head commit, its
// commits and how far it is behind its base are read from that bare repo, as GitHub reads
// its own copy; HeadOID, Commits and Behind are what it answers without one (and the head
// a merged PR keeps once its branch is deleted).
type PR struct {
	URL, Author, Head, HeadOID, Base string
	State                            string // OPEN, MERGED or CLOSED
	Draft                            bool
	CI                               []Check // gh pr checks; none registered is no checks
	Comments                         []Comment
	Reviews                          []Comment
	Fork                             string // the head's repo, owner/name, when it is a fork
	MaintainerCanModify              bool
	Commits                          []string
	Behind                           int
	// Stale keeps the head GitHub last saw: it lags a push by a few seconds (probed live,
	// bees-13.6).
	Stale bool
	// Next, when set, replaces CI once gh pr checks has answered with CI as it is: a CI run
	// that finishes after bs has seen it pending, however slow the machine.
	Next *[]Check
}

// Check is one CI check of a PR; an Optional one isn't required by branch protection, so
// gh pr checks --required leaves it out.
type Check struct {
	Name, State string // State: SUCCESS, FAILURE, PENDING, SKIPPED, CANCELLED
	Optional    bool
}

// bucket is gh pr checks' bucket for a check's state.
func (c Check) bucket() string {
	return map[string]string{"SUCCESS": "pass", "FAILURE": "fail", "PENDING": "pending", "SKIPPED": "skipping", "CANCELLED": "cancel"}[c.State]
}

// Comment is one PR comment or review.
type Comment struct {
	Author, Body string
	At           time.Time
}

// GH is a fake gh: pr view, pr checks, pr list, and the api paths bs reads (compare,
// a commit's check runs and statuses, a branch's protection). Offline makes every call
// fail as gh does with no network.
type GH struct {
	mu      sync.Mutex
	PRs     map[string]*PR
	Offline bool
	// Hang makes every call hang until its context ends, as gh does on a network that
	// drops packets.
	Hang bool
	// Git is a bare repo standing for GitHub's copy of the repo; "" answers from the PRs'
	// own fields. Each view writes refs/pull/<n>/head there, as GitHub keeps it.
	Git       string
	Protected map[string]bool // branch protection, by branch
	// Requires is the checks a ruleset requires on a branch, by branch: answered under
	// rules/branches/<b>, with branches/<b>'s classic list empty (jmmshn/bees's shape,
	// probed).
	Requires map[string][]string
}

// Edit changes PR url under the lock gh's calls take: a test's change while a bs done
// --wait polls.
func (g *GH) Edit(url string, f func(*PR)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f(g.PRs[url])
}

func ghFail(argv []string, msg string) error {
	return &port.ExitError{Argv: argv, Code: 1, Stderr: []byte(msg + "\n")}
}

// git runs git in the bare repo.
func (g *GH) git(args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", g.Git}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// refresh reads pr's head, commits and lag from the bare repo, when there is one.
func (g *GH) refresh(pr *PR) error {
	if g.Git == "" {
		return nil
	}
	if oid, err := g.git("rev-parse", "--verify", "--quiet", "refs/heads/"+pr.Head); err == nil && pr.State == "OPEN" && !pr.Stale {
		pr.HeadOID = oid
	}
	if pr.HeadOID == "" {
		return nil
	}
	n := pr.URL[strings.LastIndex(pr.URL, "/")+1:]
	if _, err := g.git("update-ref", "refs/pull/"+n+"/head", pr.HeadOID); err != nil {
		return fmt.Errorf("fake gh: refs/pull/%s/head: %w", n, err)
	}
	if out, err := g.git("rev-list", "--reverse", "refs/heads/"+pr.Base+".."+pr.HeadOID); err == nil {
		pr.Commits = strings.Fields(out)
	}
	if out, err := g.git("rev-list", "--count", pr.HeadOID+"..refs/heads/"+pr.Base); err == nil {
		pr.Behind, _ = strconv.Atoi(out)
	}
	return nil
}

func (g *GH) Run(ctx context.Context, _ string, argv ...string) ([]byte, error) {
	g.mu.Lock()
	if g.Hang {
		g.mu.Unlock()
		<-ctx.Done()
		return nil, fmt.Errorf("%s: %w", strings.Join(argv, " "), ctx.Err())
	}
	defer g.mu.Unlock()
	if g.Offline {
		return nil, ghFail(argv, "error connecting to api.github.com")
	}
	if len(argv) >= 3 && argv[1] == "api" {
		return g.api(argv)
	}
	if len(argv) >= 3 && argv[1] == "pr" && argv[2] == "list" {
		return g.list(argv)
	}
	if len(argv) < 4 || argv[1] != "pr" {
		return nil, ghFail(argv, "fake gh: only pr view, pr checks, pr list and api")
	}
	pr := g.PRs[argv[3]]
	if pr == nil {
		return nil, ghFail(argv, "GraphQL: Could not resolve to a PullRequest with the number of "+argv[3][strings.LastIndex(argv[3], "/")+1:]+". (repository.pullRequest)")
	}
	if err := g.refresh(pr); err != nil {
		return nil, ghFail(argv, err.Error())
	}
	repo := strings.TrimPrefix(pr.URL[:strings.Index(pr.URL, "/pull/")], "https://github.com/")
	commits := []any{}
	for _, c := range pr.Commits {
		commits = append(commits, map[string]string{"oid": c})
	}
	fields := map[string]any{
		"url": pr.URL, "state": pr.State, "isDraft": pr.Draft, "headRefName": pr.Head, "headRefOid": pr.HeadOID,
		"baseRefName": pr.Base, "author": map[string]string{"login": pr.Author}, "commits": commits,
		"isCrossRepository": pr.Fork != "", "maintainerCanModify": pr.MaintainerCanModify,
		"headRepository": map[string]string{"nameWithOwner": cmp.Or(pr.Fork, repo)},
	}
	cs, rs := []any{}, []any{} // gh says [] for none, never null
	for _, c := range pr.Comments {
		cs = append(cs, map[string]any{"author": map[string]string{"login": c.Author}, "body": c.Body, "createdAt": c.At.UTC().Format(time.RFC3339)})
	}
	for _, c := range pr.Reviews {
		rs = append(rs, map[string]any{"author": map[string]string{"login": c.Author}, "body": c.Body, "submittedAt": c.At.UTC().Format(time.RFC3339)})
	}
	fields["comments"], fields["reviews"] = cs, rs
	want := ""
	if i := slices.Index(argv, "--json"); i >= 0 && i+1 < len(argv) {
		want = argv[i+1]
	}
	switch argv[2] {
	case "view":
		out := map[string]any{}
		for _, f := range strings.Split(want, ",") {
			v, ok := fields[f]
			if !ok {
				return nil, ghFail(argv, "Unknown JSON field: \""+f+"\"")
			}
			out[f] = v
		}
		return json.Marshal(out)
	case "checks":
		cs := pr.CI
		if slices.Contains(argv, "--required") {
			cs = slices.DeleteFunc(slices.Clone(cs), func(c Check) bool { return c.Optional })
			if len(cs) == 0 { // probed on gh 2.102
				return nil, ghFail(argv, fmt.Sprintf("No required checks reported on the '%s' branch", pr.Head))
			}
		}
		if len(cs) == 0 {
			return nil, ghFail(argv, fmt.Sprintf("no checks reported on the '%s' branch", pr.Head))
		}
		var out []map[string]string
		defer func() {
			if pr.Next != nil {
				pr.CI, pr.Next = *pr.Next, nil
			}
		}()
		for _, c := range cs {
			row := map[string]string{}
			for _, f := range strings.Split(want, ",") {
				switch f {
				case "name":
					row[f] = c.Name
				case "state":
					row[f] = c.State
				case "bucket":
					row[f] = c.bucket()
				default:
					return nil, ghFail(argv, "Unknown JSON field: \""+f+"\"")
				}
			}
			out = append(out, row)
		}
		return json.Marshal(out)
	}
	return nil, ghFail(argv, "fake gh: no pr "+argv[2])
}

// api answers gh api repos/<o>/<r> (its default branch, main), and under it
// compare/<base>...<head>, branches/<branch> and rules/branches/<branch>; a ref or branch
// the bare repo doesn't have is a 404, as on GitHub.
func (g *GH) api(argv []string) ([]byte, error) {
	notFound := &port.ExitError{Argv: argv, Code: 1, Stderr: []byte("gh: Not Found (HTTP 404)\n")}
	parts := strings.SplitN(argv[2], "/", 4)
	if len(parts) < 3 || parts[0] != "repos" {
		return nil, ghFail(argv, "fake gh: api "+argv[2])
	}
	if len(parts) == 3 {
		return json.Marshal(map[string]string{"default_branch": "main"})
	}
	has := func(ref string) bool {
		if g.Git == "" {
			return true
		}
		_, err := g.git("rev-parse", "--verify", "--quiet", ref+"^{commit}")
		return err == nil
	}
	rest := parts[3]
	switch {
	case strings.HasPrefix(rest, "compare/"):
		base, head, _ := strings.Cut(strings.TrimPrefix(rest, "compare/"), "...")
		for _, pr := range g.PRs {
			if err := g.refresh(pr); err != nil {
				return nil, ghFail(argv, err.Error())
			}
			if pr.HeadOID == head && pr.Base == base {
				return json.Marshal(map[string]int{"behind_by": pr.Behind})
			}
		}
		if g.Git != "" && has(head) && has("refs/heads/"+base) {
			out, err := g.git("rev-list", "--count", head+"..refs/heads/"+base)
			if err != nil {
				return nil, ghFail(argv, err.Error())
			}
			n, _ := strconv.Atoi(out)
			return json.Marshal(map[string]int{"behind_by": n})
		}
		return nil, notFound
	case strings.HasPrefix(rest, "branches/"):
		b := strings.TrimPrefix(rest, "branches/")
		if !has("refs/heads/" + b) {
			return nil, notFound
		}
		return json.Marshal(map[string]any{"protected": g.Protected[b], "protection": map[string]any{
			"enabled": false, "required_status_checks": map[string]any{"contexts": []string{}, "checks": []any{}}}})
	case strings.HasPrefix(rest, "rules/branches/"):
		var checks []map[string]string
		for _, c := range g.Requires[strings.TrimPrefix(rest, "rules/branches/")] {
			checks = append(checks, map[string]string{"context": c})
		}
		if checks == nil {
			return []byte("[]"), nil
		}
		return json.Marshal([]any{map[string]any{"type": "required_status_checks", "parameters": map[string]any{"required_status_checks": checks}}})
	}
	return nil, ghFail(argv, "fake gh: api "+argv[2])
}

// list is gh pr list [--repo OWNER/REPO] --head <branch> --state <state> --json headRefOid:
// the PRs of that repo (by URL) whose head branch is branch, in state (open, merged,
// closed, all).
func (g *GH) list(argv []string) ([]byte, error) {
	flag := func(name string) string {
		if i := slices.Index(argv, name); i >= 0 && i+1 < len(argv) {
			return argv[i+1]
		}
		return ""
	}
	if f := flag("--json"); f != "headRefOid" {
		return nil, ghFail(argv, "fake gh: pr list --json headRefOid only")
	}
	head, state := flag("--head"), strings.ToUpper(cmp.Or(flag("--state"), "open"))
	out := []map[string]string{}
	for _, url := range slices.Sorted(maps.Keys(g.PRs)) {
		if repo := flag("--repo"); repo != "" && !strings.HasPrefix(url, "https://github.com/"+repo+"/pull/") {
			continue
		}
		if pr := g.PRs[url]; pr.Head == head && (state == "ALL" || pr.State == state) {
			out = append(out, map[string]string{"headRefOid": pr.HeadOID})
		}
	}
	return json.Marshal(out)
}

// CL is a fake CL tool for a CL-shaped workspace (ADR-0010 command kind): cl upload
// makes a pending change from the dir, cl status reads it, cl submit lands it.
type CL struct {
	mu   sync.Mutex
	CLs  []CLChange
	next int
}

// CLChange is one change: where it was uploaded from and its state.
type CLChange struct {
	ID, Dir, State string // State: pending or submitted
}

func (c *CL) Run(_ context.Context, dir string, argv ...string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fail := func(msg string) error { return &port.ExitError{Argv: argv, Code: 1, Stderr: []byte(msg)} }
	if len(argv) < 2 {
		return nil, fail("usage: cl upload | status <id> | submit <id>")
	}
	find := func() *CLChange {
		for i := range c.CLs {
			if len(argv) > 2 && c.CLs[i].ID == argv[2] {
				return &c.CLs[i]
			}
		}
		return nil
	}
	switch argv[1] {
	case "upload":
		c.next++
		ch := CLChange{ID: fmt.Sprintf("cl/%d", c.next), Dir: dir, State: "pending"}
		c.CLs = append(c.CLs, ch)
		return []byte(ch.ID + "\n"), nil
	case "status":
		if ch := find(); ch != nil {
			return json.Marshal(ch)
		}
	case "submit":
		if ch := find(); ch != nil {
			ch.State = "submitted"
			return nil, nil
		}
	}
	return nil, fail("no such cl")
}

// Beads is an in-memory bead tracker.
type Beads struct {
	mu    sync.Mutex
	Beads map[string]port.Bead
}

// Add stores b.
func (f *Beads) Add(b port.Bead) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Beads == nil {
		f.Beads = map[string]port.Bead{}
	}
	f.Beads[b.ID] = b
}

func (f *Beads) Show(_ context.Context, id string) (port.Bead, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.Beads[id]
	if !ok {
		return port.Bead{}, fmt.Errorf("bd show %s: %w", id, port.ErrNoBead)
	}
	return b, nil
}

func (f *Beads) Children(_ context.Context, parent string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id, b := range f.Beads {
		if b.Parent == parent {
			out = append(out, id)
		}
	}
	return out, nil // map order: bd lists them in no fixed order either
}
