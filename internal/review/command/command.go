// Package command is the command review host (review.host = "command", ADR-0010
// Amendment 6): the keeper's argv from [review.command], each printing one JSON value,
// answer for a review host bs ships no adapter for (a CL host). Every tool name, command
// and URL shape lives in the keeper's config.
package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/checks"
	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/port"
)

// Name is this host's config name.
const Name = config.CommandHost

// Runner fills c's argv from vals and runs it in dir, as checks.Run does: a non-zero exit
// is a Result, never an error.
type Runner func(ctx context.Context, c checks.Cmd, dir string, vals map[string]checks.Value) (checks.Result, error)

// Host is [review.command], run on Run.
type Host struct {
	cfg     config.ReviewCommand
	url     *regexp.Regexp
	timeout time.Duration
	run     Runner
}

var _ port.Review = Host{}

// New is the host cfg configures, run on run; nil runs each argv as the checks runner
// does, with getenv's PATH, HOME and LANG.
func New(cfg *config.ReviewCommand, run Runner, getenv func(string) string) (Host, error) {
	if err := cfg.Incomplete(); err != nil {
		return Host{}, err
	}
	re, err := config.URLPattern(cfg.URL)
	if err != nil {
		return Host{}, err
	}
	if run == nil {
		run = func(ctx context.Context, c checks.Cmd, dir string, vals map[string]checks.Value) (checks.Result, error) {
			return checks.Run(ctx, c, dir, vals, getenv)
		}
	}
	timeout, _ := time.ParseDuration(cfg.Timeout) // checked at load; 0 is the default
	return Host{cfg: *cfg, url: re, timeout: timeout, run: run}, nil
}

func (Host) Name() string { return Name }

// call fills key's argv from vals, runs it in dir and reads its stdout as JSON into v.
// A non-zero exit, or stdout that isn't JSON, is "couldn't ask" (Rule G).
func (h Host) call(ctx context.Context, key string, argv []string, dir string, vals map[string]string, v any) error {
	vs := map[string]checks.Value{}
	for k, s := range vals {
		vs[k] = checks.Value{S: s}
	}
	res, err := h.run(ctx, checks.Cmd{Argv: argv, Timeout: h.timeout}, dir, vs)
	if err != nil {
		return fmt.Errorf("[review.command] %s: %w", key, err)
	}
	if res.Code != 0 {
		return fmt.Errorf("[review.command] %s: %s exited %d: %s", key, strings.Join(res.Argv, " "), res.Code, strings.TrimSpace(string(res.Output)))
	}
	if err := json.Unmarshal(bytes.TrimSpace(res.Stdout), v); err != nil {
		return fmt.Errorf("[review.command] %s: %s printed no JSON bs reads: %w", key, strings.Join(res.Argv, " "), err)
	}
	return nil
}

// repo is the repo command's answer.
type repo struct {
	Repo      string `json:"repo"`
	Protected bool   `json:"protected"`
}

func (h Host) Repo(ctx context.Context, dir string) (string, bool, error) {
	var r *repo
	if err := h.call(ctx, "repo", h.cfg.Repo, dir, map[string]string{"repo": dir}, &r); err != nil || r == nil {
		return "", false, err
	}
	return r.Repo, r.Repo != "", nil
}

// Protected is the repo command's protected; ponytail: it answers for dir's checkout's
// repo, never repo itself, which is the only one bs asks of.
func (h Host) Protected(ctx context.Context, dir, _ string) (bool, error) {
	var r *repo
	if err := h.call(ctx, "repo", h.cfg.Repo, dir, map[string]string{"repo": dir}, &r); err != nil || r == nil {
		return false, err
	}
	return r.Protected, nil
}

// Parse matches url whole against [review.command] url: its repo and number groups.
func (h Host) Parse(s string) (port.Ref, error) {
	s = strings.TrimSpace(s)
	m := h.url.FindStringSubmatch(s)
	if m == nil {
		return port.Ref{}, fmt.Errorf("%q is not a change URL ([review.command] url %s)", s, h.cfg.URL)
	}
	repo, num := m[h.url.SubexpIndex("repo")], m[h.url.SubexpIndex("number")]
	if n, err := strconv.Atoi(num); err != nil || n <= 0 || repo == "" {
		return port.Ref{}, fmt.Errorf("%q is not a change URL: its repo %q or number %q is not one", s, repo, num)
	}
	return port.Ref{URL: s, Repo: repo}, nil
}

// change is a change as a command prints it (ADR-0010 Amendment 6).
type change struct {
	URL         string    `json:"url"`
	State       string    `json:"state"`
	Draft       bool      `json:"draft"`
	Head        string    `json:"head"`
	HeadCommit  string    `json:"head_commit"`
	Base        string    `json:"base"`
	Commits     []string  `json:"commits"`
	Foreign     bool      `json:"foreign"`
	ForeignRepo string    `json:"foreign_repo"`
	Editable    *bool     `json:"editable"` // absent: editable unless foreign
	Created     time.Time `json:"created"`
	Behind      int       `json:"behind"`
}

// port is c as bs reads it: its repo and number from its URL, never a field of its own.
func (h Host) port(key string, c change) (port.Change, error) {
	r, err := h.Parse(c.URL)
	if err != nil {
		return port.Change{}, fmt.Errorf("[review.command] %s: %w", key, err)
	}
	state := strings.ToLower(c.State)
	if state != port.Open && state != port.Merged && state != port.Closed {
		return port.Change{}, fmt.Errorf("[review.command] %s: %s has state %q, not open, merged or closed", key, c.URL, c.State)
	}
	n, _ := strconv.Atoi(h.url.FindStringSubmatch(r.URL)[h.url.SubexpIndex("number")])
	editable := !c.Foreign
	if c.Editable != nil {
		editable = *c.Editable
	}
	return port.Change{URL: r.URL, Repo: r.Repo, Number: n, State: state, Draft: c.Draft, Head: c.Head, HeadCommit: c.HeadCommit,
		Base: c.Base, Commits: c.Commits, Foreign: c.Foreign, ForeignRepo: c.ForeignRepo, Editable: editable, Created: c.Created}, nil
}

// view is the view command's answer for r; nil is the host's "no such change".
func (h Host) view(ctx context.Context, dir string, r port.Ref) (*change, error) {
	var c *change
	err := h.call(ctx, "view", h.cfg.View, dir, map[string]string{"repo": dir, "url": r.URL}, &c)
	return c, err
}

// View is the view command's change; null is port.ErrNoChange.
func (h Host) View(ctx context.Context, dir string, r port.Ref) (port.Change, error) {
	c, err := h.view(ctx, dir, r)
	if err != nil {
		return port.Change{}, err
	}
	if c == nil {
		return port.Change{}, fmt.Errorf("%w: %s", port.ErrNoChange, r.URL)
	}
	return h.port("view", *c)
}

// ci is the ci command's answer.
type ci struct {
	Checks   []port.CICheck `json:"checks"`
	Required []string       `json:"required"`
}

// port.CICheck has no JSON tags: name and bucket are its fields lowercased, which
// encoding/json matches case-insensitively.

func (h Host) ci(ctx context.Context, dir string, c port.Change) (ci, error) {
	var out ci
	err := h.call(ctx, "ci", h.cfg.CI, dir, map[string]string{"repo": dir, "url": c.URL}, &out)
	return out, err
}

func (h Host) CI(ctx context.Context, dir string, c port.Change) ([]port.CICheck, error) {
	out, err := h.ci(ctx, dir, c)
	return out.Checks, err
}

func (h Host) RequiredChecks(ctx context.Context, dir string, c port.Change) ([]string, error) {
	out, err := h.ci(ctx, dir, c)
	return out.Required, err
}

// Behind is the view command's behind.
func (h Host) Behind(ctx context.Context, dir string, c port.Change) (int, error) {
	v, err := h.view(ctx, dir, port.Ref{URL: c.URL, Repo: c.Repo})
	if err != nil {
		return 0, err
	}
	if v == nil {
		return 0, fmt.Errorf("%w: %s", port.ErrNoChange, c.URL)
	}
	return v.Behind, nil
}

// Changes is the changes command's list, foreign ones and those created before since
// dropped, newest first.
func (h Host) Changes(ctx context.Context, w port.Where, since time.Time) ([]port.Change, string, error) {
	vals := map[string]string{"repo": w.Repo, "dir": w.Dir, "branch": w.Branch, "base": w.Base}
	for k, v := range vals {
		if v == "" {
			delete(vals, k) // an argv naming it is refused, never run with "" in its place
		}
	}
	var list []change
	if err := h.call(ctx, "changes", h.cfg.Changes, w.Repo, vals, &list); err != nil {
		return nil, "", err
	}
	var cs []port.Change
	for _, l := range list {
		if l.Created.IsZero() {
			return nil, "", fmt.Errorf("[review.command] changes: %s has no created time", l.URL)
		}
		c, err := h.port("changes", l)
		if err != nil {
			return nil, "", err
		}
		if !c.Foreign && !c.Created.Before(since) {
			cs = append(cs, c)
		}
	}
	slices.SortStableFunc(cs, func(a, b port.Change) int { return b.Created.Compare(a.Created) })
	return cs, "", nil
}

// hint is text with each {key} filled from vals; a key with no value stays as written.
func hint(text string, vals map[string]string) string {
	var pairs []string
	for k, v := range vals {
		if v != "" {
			pairs = append(pairs, "{"+k+"}", v)
		}
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// Create is [review.command] create, or a generic line when it is unset.
func (h Host) Create(w port.Where) string {
	text := h.cfg.Create
	if text == "" {
		text = "upload a change from {dir} (review.fragment says how)"
	}
	return hint(text, map[string]string{"dir": w.Dir, "branch": w.Branch, "base": w.Base})
}

func (h Host) Retarget(c port.Change, base string) string {
	text := h.cfg.Retarget
	if text == "" {
		text = "move {url} onto {base}"
		if c.URL == "" {
			text = "move the change from {head} onto {base}"
		}
	}
	return hint(text, h.changeVals(c, base))
}

func (h Host) PushHint(_ context.Context, dir string, c port.Change) string {
	text := h.cfg.PushHint
	if text == "" {
		text = "upload {dir} onto {url}"
	}
	vals := h.changeVals(c, c.Base)
	vals["dir"] = dir
	return hint(text, vals)
}

func (Host) changeVals(c port.Change, base string) map[string]string {
	num := ""
	if c.Number > 0 {
		num = strconv.Itoa(c.Number)
	}
	return map[string]string{"url": c.URL, "number": num, "base": base, "head": c.Head}
}

func (h Host) MergeDeny() []string   { return slices.Clone(h.cfg.MergeDeny) }
func (h Host) ApproveDeny() []string { return slices.Clone(h.cfg.ApproveDeny) }

// Fetch runs the fetch command, its answer unread; unset, it has nothing to do.
func (h Host) Fetch(ctx context.Context, dir string, c port.Change) error {
	if len(h.cfg.Fetch) == 0 {
		return nil
	}
	var answer json.RawMessage
	return h.call(ctx, "fetch", h.cfg.Fetch, dir, map[string]string{"repo": dir, "url": c.URL, "commit": c.HeadCommit}, &answer)
}

// PostReview writes body to a file and runs post_review, which dedupes by marker.
func (h Host) PostReview(ctx context.Context, dir string, c port.Change, body, marker string) (string, error) {
	f, err := os.CreateTemp("", "bs-review-*.md")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	var out struct {
		URL string `json:"url"`
	}
	vals := map[string]string{"repo": dir, "url": c.URL, "body_file": f.Name(), "marker": marker}
	if err := h.call(ctx, "post_review", h.cfg.PostReview, dir, vals, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("[review.command] post_review printed no url")
	}
	return out.URL, nil
}
