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

	"github.com/jmmshn/bees/internal/port"
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
	// Created is when it was opened (gh's createdAt); MergeCommit the commit a merge made
	// (mergeCommit.oid), "" while unmerged.
	Created     time.Time
	MergeCommit string
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

// Comment is one PR comment or review. A review gh pr review posted has an ID and a
// State (COMMENTED, APPROVED, CHANGES_REQUESTED).
type Comment struct {
	Author, Body string
	At           time.Time
	ID           int
	State        string
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
	// Login is the account gh runs as ("jmmshn" when ""): GitHub refuses it approving or
	// requesting changes on its own PR.
	Login string
	// Calls is every argv gh was called with, oldest first.
	Calls  [][]string
	review int // the last review id given
}

func (g *GH) login() string { return cmp.Or(g.Login, "jmmshn") }

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
	g.Calls = append(g.Calls, slices.Clone(argv))
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
		return nil, ghFail(argv, "fake gh: only pr view, pr checks, pr list, pr review and api")
	}
	pr := g.PRs[argv[3]]
	if pr == nil {
		return nil, ghFail(argv, "GraphQL: Could not resolve to a PullRequest with the number of "+argv[3][strings.LastIndex(argv[3], "/")+1:]+". (repository.pullRequest)")
	}
	if err := g.refresh(pr); err != nil {
		return nil, ghFail(argv, err.Error())
	}
	want := ""
	if i := slices.Index(argv, "--json"); i >= 0 && i+1 < len(argv) {
		want = argv[i+1]
	}
	switch argv[2] {
	case "view":
		return pick(argv, g.fields(pr), want)
	case "review":
		return g.postReview(argv, pr)
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

// fields is every --json field gh pr view and gh pr list answer for pr.
func (g *GH) fields(pr *PR) map[string]any {
	repo := strings.TrimPrefix(pr.URL[:strings.Index(pr.URL, "/pull/")], "https://github.com/")
	n, _ := strconv.Atoi(pr.URL[strings.LastIndex(pr.URL, "/")+1:])
	commits := []any{}
	for _, c := range pr.Commits {
		commits = append(commits, map[string]string{"oid": c})
	}
	var merge any // gh says null for an unmerged PR's mergeCommit
	if pr.MergeCommit != "" {
		merge = map[string]string{"oid": pr.MergeCommit}
	}
	fields := map[string]any{
		"number": n, "url": pr.URL, "state": pr.State, "isDraft": pr.Draft, "headRefName": pr.Head, "headRefOid": pr.HeadOID,
		"baseRefName": pr.Base, "author": map[string]string{"login": pr.Author}, "commits": commits,
		"isCrossRepository": pr.Fork != "", "maintainerCanModify": pr.MaintainerCanModify,
		"headRepository": map[string]string{"nameWithOwner": cmp.Or(pr.Fork, repo)},
		"createdAt":      pr.Created.UTC().Format(time.RFC3339), "mergeCommit": merge,
	}
	cs, rs := []any{}, []any{} // gh says [] for none, never null
	for _, c := range pr.Comments {
		cs = append(cs, map[string]any{"author": map[string]string{"login": c.Author}, "body": c.Body, "createdAt": c.At.UTC().Format(time.RFC3339)})
	}
	for _, c := range pr.Reviews {
		rs = append(rs, map[string]any{"author": map[string]string{"login": c.Author}, "body": c.Body, "submittedAt": c.At.UTC().Format(time.RFC3339)})
	}
	fields["comments"], fields["reviews"] = cs, rs
	return fields
}

// pick is fields cut to want's comma-separated names; one gh doesn't know is its error.
func pick(argv []string, fields map[string]any, want string) ([]byte, error) {
	out := map[string]any{}
	for _, f := range strings.Split(want, ",") {
		v, ok := fields[f]
		if !ok {
			return nil, ghFail(argv, "Unknown JSON field: \""+f+"\"")
		}
		out[f] = v
	}
	return json.Marshal(out)
}

// postReview is gh pr review <url> --comment|--approve|--request-changes --body <text> (or
// -b, or --body-file <path>): a review by Login. Like GitHub, it refuses approving or
// requesting changes on Login's own PR, and like gh it prints nothing on success.
// ponytail: --body-file - (stdin) is refused: the fake's Run has no stdin.
func (g *GH) postReview(argv []string, pr *PR) ([]byte, error) {
	state := ""
	for flag, s := range map[string]string{"--comment": "COMMENTED", "-c": "COMMENTED", "--approve": "APPROVED", "-a": "APPROVED", "--request-changes": "CHANGES_REQUESTED", "-r": "CHANGES_REQUESTED"} {
		if slices.Contains(argv, flag) {
			if state != "" {
				return nil, ghFail(argv, "need exactly one of --approve, --request-changes, or --comment")
			}
			state = s
		}
	}
	if state == "" {
		return nil, ghFail(argv, "--approve, --request-changes, or --comment required when not running interactively")
	}
	body := ""
	for i, a := range argv {
		if i+1 >= len(argv) {
			break
		}
		switch a {
		case "--body", "-b":
			body = argv[i+1]
		case "--body-file", "-F":
			if argv[i+1] == "-" {
				return nil, ghFail(argv, "fake gh: --body-file - reads stdin, which the fake has none of: pass --body or a file")
			}
			b, err := os.ReadFile(argv[i+1])
			if err != nil {
				return nil, ghFail(argv, err.Error())
			}
			body = string(b)
		}
	}
	if state != "APPROVED" && strings.TrimSpace(body) == "" {
		return nil, ghFail(argv, "body cannot be blank for comment and request-changes reviews")
	}
	if pr.Author == g.login() && state != "COMMENTED" {
		verb := map[string]string{"APPROVED": "approve", "CHANGES_REQUESTED": "request changes on"}[state]
		return nil, ghFail(argv, "failed to create review: GraphQL: Review Can not "+verb+" your own pull request (addPullRequestReview)")
	}
	g.review++
	pr.Reviews = append(pr.Reviews, Comment{Author: g.login(), Body: body, At: time.Now(), ID: g.review, State: state})
	return nil, nil
}

// api answers gh api repos/<o>/<r> (its default branch, main), and under it
// compare/<base>...<head>, branches/<branch>, rules/branches/<branch> and pulls/<n>/reviews;
// a ref or branch the bare repo doesn't have is a 404, as on GitHub.
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
	case strings.HasPrefix(rest, "pulls/") && strings.HasSuffix(rest, "/reviews"):
		pr := g.PRs["https://github.com/"+parts[1]+"/"+parts[2]+"/pull/"+strings.TrimSuffix(strings.TrimPrefix(rest, "pulls/"), "/reviews")]
		if pr == nil {
			return nil, notFound
		}
		out := []any{}
		for _, r := range pr.Reviews {
			out = append(out, map[string]any{"id": r.ID, "user": map[string]string{"login": r.Author}, "body": r.Body, "state": cmp.Or(r.State, "COMMENTED"),
				"html_url": fmt.Sprintf("%s#pullrequestreview-%d", pr.URL, r.ID), "submitted_at": r.At.UTC().Format(time.RFC3339)})
		}
		return json.Marshal(out)
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

// list is gh pr list [--repo OWNER/REPO] --head <branch> [--state <state>] [--limit <n>]
// --json <fields>: the PRs of that repo (by URL) whose head branch is branch, in state
// (open by default, merged, closed, all), newest first, at most n (30 by default). Like
// gh's, --head matches a fork's PR from a branch of the same name too: only
// isCrossRepository tells them apart (bees-13.60 V2), and --head owner:branch matches
// nothing, exit 0 (probed on gh 2.102, bees-13.68).
func (g *GH) list(argv []string) ([]byte, error) {
	flag := func(name string) string {
		if i := slices.Index(argv, name); i >= 0 && i+1 < len(argv) {
			return argv[i+1]
		}
		return ""
	}
	head, state := flag("--head"), strings.ToUpper(cmp.Or(flag("--state"), "open"))
	limit, err := strconv.Atoi(cmp.Or(flag("--limit"), "30"))
	if err != nil {
		return nil, ghFail(argv, "invalid argument "+strconv.Quote(flag("--limit"))+" for \"-L, --limit\" flag")
	}
	var prs []*PR
	for _, url := range slices.Sorted(maps.Keys(g.PRs)) {
		if repo := flag("--repo"); repo != "" && !strings.HasPrefix(url, "https://github.com/"+repo+"/pull/") {
			continue
		}
		if pr := g.PRs[url]; pr.Head == head && (state == "ALL" || pr.State == state) {
			prs = append(prs, pr)
		}
	}
	slices.SortStableFunc(prs, func(a, b *PR) int { return b.Created.Compare(a.Created) })
	out := []json.RawMessage{}
	for _, pr := range prs[:min(limit, len(prs))] {
		if err := g.refresh(pr); err != nil {
			return nil, ghFail(argv, err.Error())
		}
		row, err := pick(argv, g.fields(pr), flag("--json"))
		if err != nil {
			return nil, err
		}
		out = append(out, row)
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
