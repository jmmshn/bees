package fake

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmmshn/bees/internal/port"
)

// CLHost is a review host and a workspace VCS that use neither git nor gh for the goal
// checks: CL-shaped changes (https://cl.test/<repo>/<n>) and a commit graph, all in
// memory. A workspace's commits are what Commit made there; uploading a workspace points
// a change at its head. It shows the goal checks need neither git nor GitHub (keeper,
// bees-13.6). ponytail: the workspace itself is still git_worktree's: Detach makes a plain
// git worktree, and prune removes it with git; the command workspace kind (bees-13.11)
// replaces it.
type CLHost struct {
	mu       sync.Mutex
	RepoName string // the repo every checkout belongs to; "" is none
	Offline  bool
	// TrunkProtected says trunk is protected; TrunkRequires names the checks it requires.
	TrunkProtected bool
	TrunkRequires  []string
	changes        map[int]*clChange
	parents        map[string][]string // commit → its parents
	made           map[string]clMade   // commits Commit made: where and when
	patch          map[string]string   // commit → its patch; a rebased copy keeps it, a merge has none
	heads          map[string]string   // checkout dir → its head commit
	trunk          string              // trunk's tip
	n              int
}

type clChange struct {
	port.Change
	CI       []port.CICheck  // required ones: CI answers only these
	Optional []port.CICheck  // checks no branch protection requires
	next     *[]port.CICheck // replaces CI once CI has answered (CLHost.Next)
	landed   string          // the trunk commit it landed as
	// dir and created are the workspace it was first uploaded from, and when: a real CL
	// host keys changes by client, never by a branch name (Head "clN" never names a worker).
	dir     string
	created time.Time
}

type clMade struct {
	dir string
	at  time.Time
}

var (
	_ port.Review = (*CLHost)(nil)
	_ port.VCS    = (*CLHost)(nil)
)

func (h *CLHost) init() {
	if h.parents == nil {
		h.changes, h.parents, h.made, h.heads, h.patch = map[int]*clChange{}, map[string][]string{}, map[string]clMade{}, map[string]string{}, map[string]string{}
		h.trunk = "t0"
		h.parents["t0"] = nil
	}
}

func (h *CLHost) newCommit(parents ...string) string {
	h.n++
	id := fmt.Sprintf("c%d", h.n)
	h.parents[id] = parents
	return id
}

// head is dir's head: trunk's tip for a checkout nothing moved yet.
func (h *CLHost) head(dir string) string {
	if c, ok := h.heads[dir]; ok {
		return c
	}
	return h.trunk
}

// Commit makes a commit in dir on top of its head and returns it.
func (h *CLHost) Commit(dir string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	id := h.newCommit(h.head(dir))
	h.made[id] = clMade{dir: dir, at: time.Now()}
	h.patch[id] = "p" + id
	h.heads[dir] = id
	return id
}

// Rebase replays dir's commits that onto lacks on top of onto, as a pull --rebase does:
// copies with the same patches, written by no one, and dir's head moves to the last.
func (h *CLHost) Rebase(dir, onto string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	have := h.ancestors(onto)
	var todo []string
	for c := h.head(dir); !slices.Contains(have, c); c = h.parents[c][0] {
		if len(h.parents[c]) == 1 {
			todo = append([]string{c}, todo...)
		}
	}
	head := onto
	for _, c := range todo {
		id := h.newCommit(head)
		h.patch[id] = h.patch[c]
		head = id
	}
	h.heads[dir] = head
}

// Sync brings trunk into dir: a merge commit made there.
func (h *CLHost) Sync(dir string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	id := h.newCommit(h.head(dir), h.trunk)
	h.made[id] = clMade{dir: dir, at: time.Now()}
	h.heads[dir] = id
	return id
}

// Move points dir's head at commit, as a reset or checkout does: it makes no commit.
func (h *CLHost) Move(dir, commit string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	h.heads[dir] = commit
}

// Head is dir's head commit.
func (h *CLHost) Head(dir string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	return h.head(dir)
}

// Trunk lands a commit made elsewhere on trunk.
func (h *CLHost) Trunk() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	h.trunk = h.newCommit(h.trunk)
	h.patch[h.trunk] = "p" + h.trunk
}

// Upload points change n (made open when new) at dir's head and returns its URL.
func (h *CLHost) Upload(dir string, n int, draft bool) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	c := h.changes[n]
	if c == nil {
		c = &clChange{Change: port.Change{URL: fmt.Sprintf("https://cl.test/%s/%d", h.RepoName, n), Repo: h.RepoName, Number: n,
			State: port.Open, Base: "trunk", Editable: true, Head: fmt.Sprintf("cl%d", n)}, dir: dir, created: time.Now()}
		h.changes[n] = c
	}
	c.Draft, c.HeadCommit = draft, h.head(dir)
	return c.URL
}

// Uploaded is every change first uploaded from dir at or after since, newest first: what
// a CL host answers for a workspace (Review.Changes, bees-13.62).
func (h *CLHost) Uploaded(dir string, since time.Time) []port.Change {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*clChange
	for _, c := range h.changes {
		if c.dir == dir && !c.created.Before(since) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b *clChange) int { return b.created.Compare(a.created) })
	var cs []port.Change
	for _, c := range out {
		ch := c.Change
		ch.Created = c.created
		cs = append(cs, ch)
	}
	return cs
}

// Set changes change n: its state (open, merged, closed), required CI, or notes.
func (h *CLHost) Set(n int, f func(c *port.Change, ci *[]port.CICheck)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.changes[n]
	f(&c.Change, &c.CI)
}

// SetOptional sets change n's checks that nothing requires: bs never reads them.
func (h *CLHost) SetOptional(n int, ci []port.CICheck) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.changes[n].Optional = ci
}

// Land submits change n: its head lands on trunk.
func (h *CLHost) Land(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.changes[n]
	h.trunk = h.newCommit(h.trunk, c.HeadCommit)
	c.State, c.landed = port.Merged, h.trunk
}

// ancestors is every commit reachable from c, c included.
func (h *CLHost) ancestors(c string) []string {
	var out []string
	for todo := []string{c}; len(todo) > 0; todo = todo[1:] {
		if !slices.Contains(out, todo[0]) {
			out = append(out, todo[0])
			todo = append(todo, h.parents[todo[0]]...)
		}
	}
	return out
}

func (h *CLHost) offline() error { return h.offlineIn("") }

// offlineIn is offline, and a dir that is gone refused as a real tool's cwd would be.
func (h *CLHost) offlineIn(dir string) error {
	if _, err := os.Stat(dir); dir != "" && err != nil {
		return fmt.Errorf("cl: can't run in %s: %w", dir, err)
	}
	if h.Offline {
		return &port.ExitError{Argv: []string{"cl"}, Code: 1, Stderr: []byte("cl: can't reach cl.test\n")}
	}
	return nil
}

func (*CLHost) Name() string { return "fakecl" }

func (h *CLHost) Repo(_ context.Context, _ string) (string, bool, error) {
	return h.RepoName, h.RepoName != "", nil
}

func (h *CLHost) Parse(url string) (port.Ref, error) {
	rest, ok := strings.CutPrefix(url, "https://cl.test/")
	repo, num, _ := strings.Cut(rest, "/")
	if n, err := strconv.Atoi(num); !ok || err != nil || n <= 0 || repo == "" {
		return port.Ref{}, fmt.Errorf("%q is not a change URL (https://cl.test/<repo>/<n>)", url)
	}
	return port.Ref{URL: "https://cl.test/" + repo + "/" + num, Repo: repo}, nil
}

func (h *CLHost) View(_ context.Context, dir string, r port.Ref) (port.Change, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	if err := h.offlineIn(dir); err != nil {
		return port.Change{}, err
	}
	n, _ := strconv.Atoi(r.URL[strings.LastIndex(r.URL, "/")+1:])
	c := h.changes[n]
	if c == nil || r.Repo != h.RepoName {
		return port.Change{}, fmt.Errorf("%w: %s", port.ErrNoChange, r.URL)
	}
	out := c.Change
	out.Notes = slices.Clone(c.Notes)
	out.Commits = nil
	onTrunk := h.ancestors(h.trunk)
	if c.landed != "" {
		onTrunk = h.ancestors(h.parents[c.landed][0]) // what trunk had before it landed
	}
	for _, a := range h.ancestors(c.HeadCommit) {
		if !slices.Contains(onTrunk, a) {
			out.Commits = append(out.Commits, a)
		}
	}
	return out, nil
}

func (h *CLHost) change(c port.Change) *clChange { return h.changes[c.Number] }

func (h *CLHost) CI(_ context.Context, dir string, c port.Change) ([]port.CICheck, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.offlineIn(dir); err != nil {
		return nil, err
	}
	ch := h.change(c)
	out := slices.Clone(ch.CI)
	if ch.next != nil {
		ch.CI, ch.next = *ch.next, nil
	}
	return out, nil
}

// Next replaces change n's CI once CI has answered with it as it is: a run that finishes
// after bs has seen it pending.
func (h *CLHost) Next(n int, ci []port.CICheck) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.changes[n].next = &ci
}

func (h *CLHost) RequiredChecks(_ context.Context, dir string, _ port.Change) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.TrunkRequires), h.offlineIn(dir)
}

func (h *CLHost) Behind(_ context.Context, dir string, c port.Change) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.offlineIn(dir); err != nil {
		return 0, err
	}
	have := h.ancestors(c.HeadCommit)
	n := 0
	for _, t := range h.ancestors(h.trunk) {
		if !slices.Contains(have, t) {
			n++
		}
	}
	return n, nil
}

func (h *CLHost) Protected(context.Context, string, string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.TrunkProtected, h.offline()
}

// Changes is the changes uploaded from w's workspace since: a CL host keys them by
// client, never by a branch name.
func (h *CLHost) Changes(_ context.Context, w port.Where, since time.Time) ([]port.Change, string, error) {
	h.mu.Lock()
	err := h.offline()
	h.mu.Unlock()
	if err != nil {
		return nil, "", err
	}
	return h.Uploaded(w.Dir, since), "", nil
}

func (*CLHost) Create(port.Where) string { return "cl upload" }

// MergeDeny denies the CL tool's submit.
func (*CLHost) MergeDeny() []string { return []string{"cl submit*", "cl * submit*"} }

// ApproveDeny denies the CL tool's approval.
func (*CLHost) ApproveDeny() []string { return []string{"cl approve*", "cl * approve*"} }

// Fetch has nothing to do: every commit is in memory.
func (h *CLHost) Fetch(context.Context, string, port.Change) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.offline()
}

func (*CLHost) PushHint(_ context.Context, _ string, c port.Change) string {
	return fmt.Sprintf("cl upload --change %d", c.Number)
}

func (h *CLHost) Commits(_ context.Context, dir string, since time.Time) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	var out []string
	for id, m := range h.made {
		if m.dir == dir && !m.at.Before(since) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

// Branch is the head of the workspace named name: a worker's workspace is the only
// "branch" a CL-shaped checkout has, and the change it uploads holds its commits. dir
// itself, when named name, is one even before anything moved it (at trunk's tip). Two
// workspaces of that name is an error, never the first one found.
func (h *CLHost) Branch(_ context.Context, dir string, name string) (string, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	var dirs []string
	if filepath.Base(dir) == name {
		dirs = append(dirs, dir)
	}
	for d := range h.heads {
		if filepath.Base(d) == name && d != dir {
			dirs = append(dirs, d)
		}
	}
	switch len(dirs) {
	case 0:
		return "", false, nil
	case 1:
		return h.head(dirs[0]), true, nil
	}
	slices.Sort(dirs)
	return "", false, fmt.Errorf("cl: workspace %s is ambiguous: %s", name, strings.Join(dirs, ", "))
}

// Pushed: a CL host has no local branches to push.
func (*CLHost) Pushed(context.Context, string, string) (string, bool, error) { return "", false, nil }

// PatchIDs is each commit's patch; every commit is in memory, so none is missing.
func (h *CLHost) PatchIDs(_ context.Context, _ string, commits []string) (map[string]string, []string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	ids := map[string]string{}
	for _, c := range commits {
		if p := h.patch[c]; p != "" {
			ids[c] = p
		}
	}
	return ids, nil, nil
}

func (h *CLHost) Has(_ context.Context, _ string, commit string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	_, ok := h.parents[commit]
	return ok, nil
}

func (h *CLHost) Descends(_ context.Context, _ string, ancestor, commit string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(h.ancestors(commit), ancestor), nil
}

// Detach makes dir a plain git worktree of repo (the workspace kind is git_worktree's)
// and starts its head, in memory, at commit.
func (h *CLHost) Detach(_ context.Context, repo, dir, commit string) error {
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "--quiet", "--detach", dir).CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %v: %s", err, out)
	}
	h.Move(dir, commit)
	return nil
}

func (h *CLHost) Drop(_ context.Context, repo, dir string) error {
	if out, err := exec.Command("git", "-C", repo, "worktree", "remove", "--force", dir).CombinedOutput(); err != nil {
		return errors.New(string(out))
	}
	return nil
}
