package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/checks"
	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/port"
)

// CLTool is a CL tool a keeper would script for [review.command] (ADR-0010 Amendment 6),
// answering in its documented JSON from CLHost's state: CLHost's first user through the
// command review host. Run is a review command Runner.
type CLTool struct{ H *CLHost }

// CLToolConfig is the [review.command] that drives CLTool: its program is "cl".
func CLToolConfig() *config.ReviewCommand {
	return &config.ReviewCommand{
		URL:         `https://cl\.test/(?P<repo>[^/]+)/(?P<number>[0-9]+)`,
		Repo:        []string{"cl", "repo"},
		Changes:     []string{"cl", "changes", "{dir}"},
		View:        []string{"cl", "view", "{url}"},
		CI:          []string{"cl", "ci", "{url}"},
		PostReview:  []string{"cl", "review", "{url}", "{body_file}", "{marker}"},
		Fetch:       []string{"cl", "fetch", "{url}"},
		Create:      "cl upload",
		Retarget:    "cl rebase --change {number} --onto {base}",
		PushHint:    "cl upload --change {number}",
		MergeDeny:   []string{"cl submit*", "cl * submit*"},
		ApproveDeny: []string{"cl approve*", "cl * approve*"},
	}
}

// clJSON is a change as CLTool prints it.
type clJSON struct {
	URL         string    `json:"url"`
	State       string    `json:"state"`
	Draft       bool      `json:"draft"`
	Head        string    `json:"head"`
	HeadCommit  string    `json:"head_commit"`
	Base        string    `json:"base"`
	Commits     []string  `json:"commits,omitempty"`
	Foreign     bool      `json:"foreign"`
	ForeignRepo string    `json:"foreign_repo,omitempty"`
	Editable    bool      `json:"editable"`
	Created     time.Time `json:"created,omitzero"`
	Behind      int       `json:"behind"`
}

func toJSON(c port.Change) clJSON {
	return clJSON{URL: c.URL, State: c.State, Draft: c.Draft, Head: c.Head, HeadCommit: c.HeadCommit, Base: c.Base, Commits: c.Commits,
		Foreign: c.Foreign, ForeignRepo: c.ForeignRepo, Editable: c.Editable, Created: c.Created}
}

func (t CLTool) Run(ctx context.Context, dir string, argv []string, _ time.Duration) (checks.Result, error) {
	out, err := t.answer(ctx, dir, argv)
	if err != nil {
		return checks.Result{Argv: argv, Code: 1, Output: []byte("cl: " + err.Error() + "\n")}, nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return checks.Result{}, err
	}
	return checks.Result{Argv: argv, Output: b, Stdout: b}, nil
}

// view is the change at url, nil when the host has none.
func (t CLTool) view(ctx context.Context, dir, url string) (*port.Change, error) {
	r, err := t.H.Parse(url)
	if err != nil {
		return nil, err
	}
	c, err := t.H.View(ctx, dir, r)
	if errors.Is(err, port.ErrNoChange) {
		return nil, nil
	}
	return &c, err
}

func (t CLTool) answer(ctx context.Context, dir string, argv []string) (any, error) {
	if len(argv) < 2 || argv[0] != "cl" {
		return nil, fmt.Errorf("usage: cl repo|changes|view|ci|review|fetch ...: %q", argv)
	}
	arg := func(i int) string {
		if i+1 < len(argv) {
			return argv[i+1]
		}
		return ""
	}
	switch argv[1] {
	case "repo":
		if t.H.RepoName == "" {
			return nil, nil
		}
		return map[string]any{"repo": t.H.RepoName, "protected": t.H.TrunkProtected}, nil
	case "changes":
		cs, _, err := t.H.Changes(ctx, port.Where{Dir: arg(1)}, time.Time{})
		var out []clJSON
		for _, c := range cs {
			out = append(out, toJSON(c))
		}
		return out, err
	case "view":
		c, err := t.view(ctx, dir, arg(1))
		if err != nil || c == nil {
			return nil, err
		}
		j := toJSON(*c)
		j.Behind, err = t.H.Behind(ctx, dir, *c)
		return j, err
	case "ci":
		c, err := t.view(ctx, dir, arg(1))
		if err != nil || c == nil {
			return nil, cmpErr(err, "no such change "+arg(1))
		}
		cs, err := t.H.CI(ctx, dir, *c)
		if err != nil {
			return nil, err
		}
		req, err := t.H.RequiredChecks(ctx, dir, *c)
		checks := []map[string]string{}
		for _, ch := range cs {
			checks = append(checks, map[string]string{"name": ch.Name, "bucket": ch.Bucket})
		}
		return map[string]any{"checks": checks, "required": req}, err
	case "review":
		c, err := t.view(ctx, dir, arg(1))
		if err != nil || c == nil {
			return nil, cmpErr(err, "no such change "+arg(1))
		}
		body, err := os.ReadFile(arg(2))
		if err != nil {
			return nil, err
		}
		u, err := t.H.PostReview(ctx, dir, *c, string(body), arg(3))
		return map[string]string{"url": u}, err
	case "fetch":
		return map[string]string{}, t.H.Fetch(ctx, dir, port.Change{})
	}
	return nil, fmt.Errorf("unknown command %s", strings.Join(argv[1:], " "))
}

// cmpErr is err, or a new one saying msg when there is none.
func cmpErr(err error, msg string) error {
	if err != nil {
		return err
	}
	return errors.New(msg)
}
