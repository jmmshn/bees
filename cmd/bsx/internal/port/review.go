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
	State      string // open, merged or closed
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

// Note is one comment or review on a change.
type Note struct {
	Body string
	At   time.Time
}

// CICheck is one CI check of a change. Bucket is pass, fail, pending, skipping or cancel.
type CICheck struct{ Name, Bucket string }

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
	// CI is the change's CI checks; none registered is an empty list.
	CI(ctx context.Context, dir string, c Change) ([]CICheck, error)
	// BaseCI is how many CI checks the tip of c's base has.
	BaseCI(ctx context.Context, dir string, c Change) (int, error)
	// Behind is how many commits c's base has that its head lacks.
	Behind(ctx context.Context, dir string, c Change) (int, error)
	// Protected reports whether the host protects repo's branch.
	Protected(ctx context.Context, dir, repo, branch string) (bool, error)
	// Fetch makes c's head commit present in dir's checkout, writing no ref.
	Fetch(ctx context.Context, dir string, c Change) error
	// PushHint is the command that pushes the HEAD of dir's checkout onto c's head.
	PushHint(ctx context.Context, dir string, c Change) string
}

// VCS is a workspace's version control as the goal checks read it.
type VCS interface {
	// Commits is every commit made in dir since since: never one checked out, reset to,
	// pulled or fast-forwarded there (ADR-0010's workspace commits).
	Commits(ctx context.Context, dir string, since time.Time) ([]string, error)
	// Branch is the tip of dir's local branch name; ok false when there is none.
	Branch(ctx context.Context, dir, name string) (tip string, ok bool, err error)
	// Has reports whether dir's checkout has commit.
	Has(ctx context.Context, dir, commit string) (bool, error)
	// Descends reports whether commit is ancestor or descends from it.
	Descends(ctx context.Context, dir, ancestor, commit string) (bool, error)
	// Detach makes dir a checkout of repo at commit on no branch: a push goal's workspace.
	Detach(ctx context.Context, repo, dir, commit string) error
	// Drop removes a checkout Detach made.
	Drop(ctx context.Context, repo, dir string) error
}
