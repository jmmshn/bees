package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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

// PR is a pull request as the fake gh holds it.
type PR struct {
	URL, Author, Head, HeadOID, Base string
	State                            string // OPEN, MERGED or CLOSED
	Draft                            bool
	CI                               []Check // gh pr checks; none registered is no checks
	Comments                         []Comment
}

// Check is one CI check of a PR.
type Check struct{ Name, State string } // State: SUCCESS, FAILURE, PENDING, SKIPPED

// Comment is one PR comment.
type Comment struct {
	Author, Body string
	At           time.Time
}

// GH is a fake gh for pr view and pr checks. Offline makes every call fail as gh does
// with no network.
type GH struct {
	mu      sync.Mutex
	PRs     map[string]*PR
	Offline bool
}

func ghFail(argv []string, msg string) error {
	return &port.ExitError{Argv: argv, Code: 1, Stderr: []byte(msg + "\n")}
}

func (g *GH) Run(_ context.Context, _ string, argv ...string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Offline {
		return nil, ghFail(argv, "error connecting to api.github.com")
	}
	if len(argv) < 4 || argv[1] != "pr" {
		return nil, ghFail(argv, "fake gh: only pr view and pr checks")
	}
	pr := g.PRs[argv[3]]
	if pr == nil {
		return nil, ghFail(argv, "GraphQL: Could not resolve to a PullRequest with the number of "+argv[3])
	}
	fields := map[string]any{
		"url": pr.URL, "state": pr.State, "isDraft": pr.Draft, "headRefName": pr.Head, "headRefOid": pr.HeadOID,
		"baseRefName": pr.Base, "author": map[string]string{"login": pr.Author},
	}
	var cs []any
	for _, c := range pr.Comments {
		cs = append(cs, map[string]any{"author": map[string]string{"login": c.Author}, "body": c.Body, "createdAt": c.At.UTC().Format(time.RFC3339)})
	}
	fields["comments"] = cs
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
		if len(pr.CI) == 0 {
			return nil, ghFail(argv, fmt.Sprintf("no checks reported on the '%s' branch", pr.Head))
		}
		var out []map[string]string
		for _, c := range pr.CI {
			out = append(out, map[string]string{"name": c.Name, "state": c.State})
		}
		return json.Marshal(out)
	}
	return nil, ghFail(argv, "fake gh: no pr "+argv[2])
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
		return port.Bead{}, fmt.Errorf("bd show %s: no such bead", id)
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
	slices.Sort(out)
	return out, nil
}
