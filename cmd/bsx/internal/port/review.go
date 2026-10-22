package port

import (
	"context"
	"errors"
	"time"
)

// ErrNoChange is a review host's definite answer that a change doesn't exist. Any other
// error from a host is "couldn't look": exit 6, never a refusal (Rule G).
var ErrNoChange = errors.New("no such change")

// Change is one change under review (a GitHub pull request, a CL) as its host has it now.
type Change struct {
	URL        string // the one spelling bs records (motif 3)
	Repo       string // the host repo it belongs to
	Number     int
	State      string // Open, Merged or Closed
	Draft      bool
	Head       string // the branch its commits are pushed to; "" when the host has none
	HeadCommit string
	Base       string   // what it lands on
	Commits    []string // the commits it would land
	// Foreign: its head lives outside Repo (a fork); Editable: the project may push to it.
	Foreign     bool
	ForeignRepo string
	Editable    bool
	Notes       []Note // comments and reviews
}

// A change's states.
const (
	Open   = "open"
	Merged = "merged"
	Closed = "closed"
)

// Note is one comment or review on a change.
type Note struct {
	Body string
	At   time.Time
}

// CICheck is one CI check of a change. Bucket is one of the CI buckets below; any other
// (gh's fail, cancel) is a failure.
type CICheck struct{ Name, Bucket string }

// The CI buckets bs reads: gh pr checks' own words.
const (
	CIPass     = "pass"
	CISkipping = "skipping"
	CIPending  = "pending"
)

// Ref is a change URL as its host reads it.
type Ref struct {
	URL  string // canonical
	Repo string
}

// Review is the host a project's changes are reviewed on and land through (ADR-0010
// goals; review.host): GitHub through gh by default. The goal checks see only this and
// VCS, so a CL host plugs in without touching them.
type Review interface {
	Name() string
	// Repo is the host repo dir's checkout belongs to; ok false when it has none.
	Repo(ctx context.Context, dir string) (repo string, ok bool, err error)
	// Parse reads a change URL of this host: never a lookup.
	Parse(url string) (Ref, error)
	// View is the change now; ErrNoChange when the host says there is none.
	View(ctx context.Context, dir string, r Ref) (Change, error)
	// CI is the change's required CI checks (keeper, 2026-10-22); none is an empty list.
	CI(ctx context.Context, dir string, c Change) ([]CICheck, error)
	// RequiredChecks is the names of the checks c's base requires, as the host's branch
	// protection says: what CI must report before none reported reads as "none required".
	RequiredChecks(ctx context.Context, dir string, c Change) ([]string, error)
	// Behind is how many commits c's base has that its head lacks.
	Behind(ctx context.Context, dir string, c Change) (int, error)
	// Protected reports whether the host protects repo's default branch.
	Protected(ctx context.Context, dir, repo string) (bool, error)
	// Landed is the head commit of each merged change whose head is dir's branch: the
	// third proof that a branch landed, after a squash or rebase (none, with no repo).
	// skipped says why the host wasn't asked of a repo it can't read.
	Landed(ctx context.Context, dir, branch string) (heads []string, skipped string, err error)
	// MergeDeny is the guard globs that keep a worker from landing a change itself on this
	// host (ADR-0010 guards: no merge); allow_merge_into lifts them.
	MergeDeny() []string
	// ApproveDeny is the guard globs that keep a worker from approving a change, which no
	// grant lifts.
	ApproveDeny() []string
	// Fetch makes c's head commit present in dir's checkout, writing no ref.
	Fetch(ctx context.Context, dir string, c Change) error
	// PushHint is the command that pushes the HEAD of dir's checkout onto c's head.
	PushHint(ctx context.Context, dir string, c Change) string
}

// VCS is a workspace's version control as the goal checks read it.
type VCS interface {
	// Commits is every commit written in dir since since: never one checked out, reset to,
	// pulled, fast-forwarded or rebased there (ADR-0010's workspace commits, with PatchIDs).
	Commits(ctx context.Context, dir string, since time.Time) ([]string, error)
	// PatchIDs is the patch of each of commits dir has, the same for a commit and every
	// rebased copy of it (none for a commit with no patch of its own, a merge); missing is
	// those dir doesn't have.
	PatchIDs(ctx context.Context, dir string, commits []string) (ids map[string]string, missing []string, err error)
	// Branch is the tip of dir's local branch name; ok false when there is none.
	Branch(ctx context.Context, dir, name string) (tip string, ok bool, err error)
	// Pushed is where dir's checkout last pushed branch name to, local or not (a push
	// from a detached HEAD too), as the checkout last saw it; ok false when it can't say.
	Pushed(ctx context.Context, dir, name string) (tip string, ok bool, err error)
	// Has reports whether dir's checkout has commit.
	Has(ctx context.Context, dir, commit string) (bool, error)
	// Descends reports whether commit is ancestor or descends from it.
	Descends(ctx context.Context, dir, ancestor, commit string) (bool, error)
	// Detach makes dir a checkout of repo at commit on no branch: a push goal's workspace.
	Detach(ctx context.Context, repo, dir, commit string) error
	// Drop removes a checkout Detach made.
	Drop(ctx context.Context, repo, dir string) error
}
