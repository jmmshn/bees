// Package github is the default review host (review.host = "github"): GitHub pull
// requests, read through gh, with the repo's git remotes saying which GitHub repo a
// checkout belongs to.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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
	Comments []struct {
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"createdAt"`
	} `json:"comments"`
	Reviews []struct {
		Body        string    `json:"body"`
		SubmittedAt time.Time `json:"submittedAt"`
	} `json:"reviews"`
}

// ponytail: gh pr view may read only one GraphQL page (100) of commits, comments and
// reviews (not probed); past that pr_workspace_commit and review_comment may miss one. gh api --paginate on
// pulls/<n>/commits, issues/<n>/comments and pulls/<n>/reviews would replace it.
const fields = "url,state,isDraft,headRefName,headRefOid,baseRefName,isCrossRepository,maintainerCanModify,headRepository,commits,comments,reviews"

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
	for _, cm := range v.Comments {
		c.Notes = append(c.Notes, port.Note{Body: cm.Body, At: cm.CreatedAt})
	}
	for _, rv := range v.Reviews {
		c.Notes = append(c.Notes, port.Note{Body: rv.Body, At: rv.SubmittedAt})
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

// Landed is gh pr list --repo <origin's> --state merged --head branch: origin's repo, never
// the upstream gh would pick for a fork. With no origin no pull request could have merged
// it; an origin not on github.com (an ssh alias, GitHub Enterprise) is skipped, and says so.
func (h Host) Landed(ctx context.Context, dir, branch string) ([]string, string, error) {
	out, err := h.Run.Run(ctx, dir, "git", "-C", dir, "config", "--get", "remote.origin.url")
	var ee *port.ExitError
	if errors.As(err, &ee) && ee.Code == 1 {
		return nil, "", nil
	} else if err != nil {
		return nil, "", err
	}
	origin := strings.TrimSpace(string(out))
	repo, ok := RepoOf(origin)
	if !ok {
		return nil, "origin " + origin + " is not on github.com, so no merged pull request was looked for", nil
	}
	out, err = h.gh(ctx, dir, "pr", "list", "--repo", repo, "--head", branch, "--state", "merged", "--json", "headRefOid")
	if err != nil {
		return nil, "", err
	}
	var prs []struct {
		OID string `json:"headRefOid"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, "", fmt.Errorf("gh pr list: %w", err)
	}
	var heads []string
	for _, p := range prs {
		heads = append(heads, p.OID)
	}
	return heads, "", nil
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
