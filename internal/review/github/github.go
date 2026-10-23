// Package github is the default review host (review.host = "github"): GitHub pull
// requests, read through gh, with the repo's git remotes saying which GitHub repo a
// checkout belongs to.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/port"
)

// Name is this host's config name.
const Name = "github"

// Host is GitHub through gh and git, run on Run.
type Host struct{ Run port.Runner }

var _ port.Review = Host{}

func (Host) Name() string { return Name }

// Parse reads https://github.com/<owner>/<name>/pull/<n>, anything after the number
// (/files, a #fragment) dropped.
func (Host) Parse(s string) (port.Ref, error) {
	bad := fmt.Errorf("%q is not a GitHub pull request URL (https://github.com/<owner>/<repo>/pull/<n>)", s)
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") {
		return port.Ref{}, bad
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" || parts[0] == "" || parts[1] == "" {
		return port.Ref{}, bad
	}
	if n, err := strconv.Atoi(parts[3]); err != nil || n <= 0 {
		return port.Ref{}, bad
	}
	repo := parts[0] + "/" + parts[1]
	return port.Ref{URL: "https://github.com/" + repo + "/pull/" + parts[3], Repo: repo}, nil
}

// RepoOf is the owner/name a git remote URL names on github.com: scp form
// (git@github.com:o/r.git), ssh:// and https://.
func RepoOf(remote string) (string, bool) {
	remote = strings.TrimSpace(remote)
	var path string
	if rest, ok := strings.CutPrefix(remote, "git@github.com:"); ok {
		path = rest
	} else if u, err := url.Parse(remote); err == nil && strings.EqualFold(u.Hostname(), "github.com") && (u.Scheme == "https" || u.Scheme == "ssh" || u.Scheme == "git") {
		path = u.Path
	} else {
		return "", false
	}
	parts := strings.Split(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

// remotes is dir's git remotes on GitHub, name → owner/name, origin first.
func (h Host) remotes(ctx context.Context, dir string) ([][2]string, error) {
	out, err := h.Run.Run(ctx, dir, "git", "-C", dir, "config", "--get-regexp", `^remote\..*\.url$`)
	var ee *port.ExitError
	if errors.As(err, &ee) && ee.Code == 1 {
		return nil, nil // no remotes
	} else if err != nil {
		return nil, err
	}
	var rs [][2]string
	for line := range strings.Lines(string(out)) {
		key, val, _ := strings.Cut(strings.TrimSpace(line), " ")
		name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
		if gh, ok := RepoOf(val); ok {
			if name == "origin" {
				rs = append([][2]string{{name, gh}}, rs...)
			} else {
				rs = append(rs, [2]string{name, gh})
			}
		}
	}
	return rs, nil
}

// Repo is the GitHub repo of origin when origin is on GitHub, else of the first remote
// that is.
func (h Host) Repo(ctx context.Context, dir string) (string, bool, error) {
	rs, err := h.remotes(ctx, dir)
	if err != nil || len(rs) == 0 {
		return "", false, err
	}
	return rs[0][1], true, nil
}

// view is gh pr view's answer, the fields bs reads.
type view struct {
	URL                 string `json:"url"`
	State               string `json:"state"`
	IsDraft             bool   `json:"isDraft"`
	HeadRefName         string `json:"headRefName"`
	HeadRefOid          string `json:"headRefOid"`
	BaseRefName         string `json:"baseRefName"`
	IsCrossRepository   bool   `json:"isCrossRepository"`
	MaintainerCanModify bool   `json:"maintainerCanModify"`
	HeadRepository      struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"headRepository"`
	Commits []struct {
		Oid string `json:"oid"`
	} `json:"commits"`
}

// ponytail: gh pr view may read only one GraphQL page (100) of commits (not probed).
const fields = "url,state,isDraft,headRefName,headRefOid,baseRefName,isCrossRepository,maintainerCanModify,headRepository,commits"

// gh runs gh in dir. gh saying the PR doesn't exist is port.ErrNoChange; any other
// failure is returned as is, and read as "couldn't look": "Could not resolve to a
// Repository" too, since GitHub says it of a private repo the token can't see (exit 6,
// never 2).
func (h Host) gh(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, err := h.Run.Run(ctx, dir, append([]string{"gh"}, args...)...)
	if said(err, "Could not resolve to a PullRequest") {
		return nil, fmt.Errorf("%w: %v", port.ErrNoChange, err)
	}
	return out, err
}

// said reports whether err is gh having run and said text.
func said(err error, text string) bool {
	var ee *port.ExitError
	return errors.As(err, &ee) && strings.Contains(string(ee.Stderr)+string(ee.Stdout), text)
}

func (h Host) View(ctx context.Context, dir string, r port.Ref) (port.Change, error) {
	out, err := h.gh(ctx, dir, "pr", "view", r.URL, "--json", fields)
	if err != nil {
		return port.Change{}, err
	}
	var v view
	if err := json.Unmarshal(out, &v); err != nil {
		return port.Change{}, fmt.Errorf("gh pr view %s: %w", r.URL, err)
	}
	n, _ := strconv.Atoi(r.URL[strings.LastIndex(r.URL, "/")+1:])
	c := port.Change{URL: r.URL, Repo: r.Repo, Number: n, State: strings.ToLower(v.State), Draft: v.IsDraft,
		Head: v.HeadRefName, HeadCommit: v.HeadRefOid, Base: v.BaseRefName,
		Foreign: v.IsCrossRepository, ForeignRepo: v.HeadRepository.NameWithOwner, Editable: !v.IsCrossRepository || v.MaintainerCanModify}
	for _, cm := range v.Commits {
		c.Commits = append(c.Commits, cm.Oid)
	}
	return c, nil
}

// CI is gh pr checks --required; "No required checks reported on the '<head>' branch"
// (probed on gh 2.102) is none.
func (h Host) CI(ctx context.Context, dir string, c port.Change) ([]port.CICheck, error) {
	out, err := h.gh(ctx, dir, "pr", "checks", c.URL, "--required", "--json", "name,bucket")
	if said(err, "equired checks reported") || said(err, "no checks reported") {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var cs []port.CICheck
	if err := json.Unmarshal(out, &cs); err != nil {
		return nil, fmt.Errorf("gh pr checks %s: %w", c.URL, err)
	}
	return cs, nil
}

// api is gh api path, its JSON answer read into v.
func (h Host) api(ctx context.Context, dir, path string, v any) error {
	out, err := h.gh(ctx, dir, "api", path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("gh api %s: %w", path, err)
	}
	return nil
}

// RequiredChecks is the required status checks of c's base: its classic branch protection
// (gh api repos/R/branches/B, which needs no admin, unlike …/protection) and its rulesets'
// (repos/R/rules/branches/B); a repo protects with either (probed: jmmshn/bees requires
// lint and test by a ruleset, its classic protection empty).
func (h Host) RequiredChecks(ctx context.Context, dir string, c port.Change) ([]string, error) {
	var b struct {
		Protection struct {
			Required struct {
				Contexts []string `json:"contexts"`
				Checks   []struct {
					Context string `json:"context"`
				} `json:"checks"`
			} `json:"required_status_checks"`
		} `json:"protection"`
	}
	base := url.PathEscape(c.Base)
	if err := h.api(ctx, dir, "repos/"+c.Repo+"/branches/"+base, &b); err != nil {
		return nil, err
	}
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			Required []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if err := h.api(ctx, dir, "repos/"+c.Repo+"/rules/branches/"+base, &rules); err != nil {
		return nil, err
	}
	names := slices.Clone(b.Protection.Required.Contexts)
	for _, ch := range b.Protection.Required.Checks {
		names = append(names, ch.Context)
	}
	for _, r := range rules {
		if r.Type == "required_status_checks" {
			for _, ch := range r.Parameters.Required {
				names = append(names, ch.Context)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

func (h Host) Behind(ctx context.Context, dir string, c port.Change) (int, error) {
	var v struct {
		Behind int `json:"behind_by"`
	}
	err := h.api(ctx, dir, "repos/"+c.Repo+"/compare/"+url.PathEscape(c.Base)+"..."+c.HeadCommit, &v)
	return v.Behind, err
}

// Protected asks GitHub for repo's default branch, then whether it is protected.
func (h Host) Protected(ctx context.Context, dir, repo string) (bool, error) {
	var r struct {
		Default string `json:"default_branch"`
	}
	if err := h.api(ctx, dir, "repos/"+repo, &r); err != nil {
		return false, err
	}
	var v struct {
		Protected bool `json:"protected"`
	}
	err := h.api(ctx, dir, "repos/"+repo+"/branches/"+url.PathEscape(r.Default), &v)
	return v.Protected, err
}

// listFields is what Changes reads of each pull request gh pr list finds.
const listFields = "number,url,state,isDraft,headRefName,headRefOid,baseRefName,isCrossRepository,createdAt"

// Changes is gh pr list --repo <origin's> --head <branch> --state all: origin's repo, never
// the upstream gh would pick for a fork. gh's --head also matches a fork's pull request
// from a branch of the same name (and refuses owner:branch), so isCrossRepository drops
// those (bees-13.60 V2). --limit 100 bounds one page; a branch has a handful at most. With no
// origin no pull request could come from it; an origin not on github.com (an ssh alias,
// GitHub Enterprise) is skipped, and says so.
func (h Host) Changes(ctx context.Context, w port.Where, since time.Time) ([]port.Change, string, error) {
	if w.Branch == "" {
		return nil, "", nil
	}
	out, err := h.Run.Run(ctx, w.Repo, "git", "-C", w.Repo, "config", "--get", "remote.origin.url")
	var ee *port.ExitError
	if errors.As(err, &ee) && ee.Code == 1 {
		return nil, "", nil
	} else if err != nil {
		return nil, "", err
	}
	origin := strings.TrimSpace(string(out))
	repo, ok := RepoOf(origin)
	if !ok {
		return nil, "origin " + origin + " is not on github.com, so no pull request was looked for", nil
	}
	out, err = h.gh(ctx, w.Repo, "pr", "list", "--repo", repo, "--head", w.Branch, "--state", "all", "--limit", "100", "--json", listFields)
	if err != nil {
		return nil, "", err
	}
	var prs []struct {
		view
		Number    int       `json:"number"`
		CreatedAt time.Time `json:"createdAt"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, "", fmt.Errorf("gh pr list: %w", err)
	}
	var cs []port.Change
	for _, p := range prs {
		if p.IsCrossRepository || p.CreatedAt.Before(since) {
			continue
		}
		cs = append(cs, port.Change{URL: p.URL, Repo: repo, Number: p.Number, State: strings.ToLower(p.State), Draft: p.IsDraft,
			Head: p.HeadRefName, HeadCommit: p.HeadRefOid, Base: p.BaseRefName, Editable: true, Created: p.CreatedAt})
	}
	slices.SortStableFunc(cs, func(a, b port.Change) int { return b.Created.Compare(a.Created) })
	return cs, "", nil
}

// Create opens a pull request from the branch, once pushed.
func (Host) Create(w port.Where) string {
	return "gh pr create, once " + w.Branch + " is pushed (git push -u origin " + w.Branch + ")"
}

// ghs is each glob as gh, command gh or a path to gh (*/gh) starts it.
func ghs(globs ...string) []string {
	var out []string
	for _, gh := range []string{"gh", "command gh", "*/gh"} {
		for _, g := range globs {
			out = append(out, gh+" "+g)
		}
	}
	return out
}

// MergeDeny denies gh's merge, whatever the spelling: gh pr merge (--auto too), gh -R o/r
// pr merge, the REST merge endpoint and GraphQL's merge mutations; never a read such as
// gh api …/pulls/41 --jq .mergeable. ponytail: a request body from a file (--input) passes;
// a deny hook that parses the command line would catch it.
func (Host) MergeDeny() []string {
	return ghs("pr merge*", "* pr merge*", "api *pulls/*/merge*", "api *merges*", "api graphql*mergePullRequest*", "api graphql*AutoMerge*")
}

// ApproveDeny denies an approving review, gh -R o/r spelled too: on a repo that requires
// one, the merge's other half. The API's: an event=APPROVE field on a reviews call, or
// GraphQL's (ponytail: the same --input gap as MergeDeny).
func (Host) ApproveDeny() []string {
	var deny []string
	for _, review := range []string{"pr review*", "* pr review*"} {
		deny = append(deny, review+" --approve*", review+" -a *", review+" -a")
	}
	return ghs(append(deny, "api *reviews*APPROVE*", "api *APPROVE*reviews*", "api graphql*APPROVE*")...)
}

// Fetch fetches refs/pull/<n>/head from the remote of c's repo, writing no ref.
func (h Host) Fetch(ctx context.Context, dir string, c port.Change) error {
	rs, err := h.remotes(ctx, dir)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if strings.EqualFold(r[1], c.Repo) {
			_, err := h.Run.Run(ctx, dir, "git", "-C", dir, "fetch", "--quiet", "--no-write-fetch-head", r[0], fmt.Sprintf("refs/pull/%d/head", c.Number))
			return err
		}
	}
	return fmt.Errorf("%s has no remote for %s", dir, c.Repo)
}

// PushHint pushes to the fork's URL for a fork's PR, else to dir's remote of c's repo.
func (h Host) PushHint(ctx context.Context, dir string, c port.Change) string {
	to := "<the remote of " + c.Repo + ">"
	if c.Foreign {
		to = "git@github.com:" + c.ForeignRepo + ".git"
	} else if rs, err := h.remotes(ctx, dir); err == nil {
		for _, r := range rs {
			if strings.EqualFold(r[1], c.Repo) {
				to = r[0]
				break
			}
		}
	}
	return "git push " + to + " HEAD:" + c.Head
}

// PostReview is gh pr review <url> --comment --body-file <file>; gh prints no URL, so the
// review is found by its marker in gh api repos/R/pulls/N/reviews, before posting (a
// retry) and after.
func (h Host) PostReview(ctx context.Context, dir string, c port.Change, body, marker string) (string, error) {
	if u, err := h.reviewWith(ctx, dir, c, marker); err != nil || u != "" {
		return u, err
	}
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
	if _, err := h.gh(ctx, dir, "pr", "review", c.URL, "--comment", "--body-file", f.Name()); err != nil {
		return "", err
	}
	u, err := h.reviewWith(ctx, dir, c, marker)
	if err == nil && u == "" {
		err = fmt.Errorf("gh pr review %s succeeded, but no review on it carries %s", c.URL, marker)
	}
	return u, err
}

// reviewWith is the URL of the review on c whose body carries marker; "" when none does.
// --paginate prints one JSON array per page.
func (h Host) reviewWith(ctx context.Context, dir string, c port.Change, marker string) (string, error) {
	path := fmt.Sprintf("repos/%s/pulls/%d/reviews", c.Repo, c.Number)
	out, err := h.gh(ctx, dir, "api", path, "--paginate")
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var page []struct {
			Body string `json:"body"`
			URL  string `json:"html_url"`
		}
		if err := dec.Decode(&page); err != nil {
			return "", fmt.Errorf("gh api %s: %w", path, err)
		}
		for _, r := range page {
			if strings.Contains(r.Body, marker) {
				return r.URL, nil
			}
		}
	}
	return "", nil
}
