// Package guards builds what a worker is denied, and what its harness's classifier may
// let it do, from the [delivery] and [guards] config (ADR-0009). The rules are
// harness-neutral; each harness renders them (Claude Code: permissions.deny and autoMode).
// They are a speed bump, not the guard: a shell finds other spellings, and branch
// protection, or the VCS's own review gate, is the real one (docs/SETUP.md).
package guards

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/jmmshn/bees/internal/config"
)

// Rules are one launch's guards.
type Rules struct {
	// Deny holds shell globs over a whole command line, where * matches any text, spaces
	// and slashes too, and nothing else is special.
	Deny []string
	// Environment and Allow are prose for a harness that judges commands by intent: where
	// the session runs and what it owns, and what it may do there.
	Environment, Allow []string
}

// Worker names what one worker's rules are about: its own git worktree on its own branch
// of Repo. A worker owns its worktree and branch, so it may build, test and push its own
// branch (bees-1.51).
type Worker struct {
	Branch, Worktree, Repo string
	// Dir is the directory of a worker with no worktree or branch (workspace kind none),
	// opened on the repo; "" for a worktree.
	Dir string
	// Protected is GitHub's word that the default branch is protected. Only then does the
	// environment say the keeper merges by hand: unchecked, that claim would relax a
	// classifier on pushes (bees-7.4).
	Protected bool
	// DefaultBranches are the names a repo's default branch may have: main and master, plus
	// what the repo says. Every one is denied, so a repo whose default is develop guards
	// develop (bees-11.7).
	DefaultBranches []string
}

// DefaultBranches is the list Worker.DefaultBranches wants: main, master and each of
// heads (origin's HEAD, the main checkout's branch; "" when unknown), without repeats.
func DefaultBranches(heads ...string) []string {
	d := []string{"main", "master"}
	for _, h := range heads {
		if h != "" && !slices.Contains(d, h) {
			d = append(d, h)
		}
	}
	return d
}

// Build returns w's rules. An empty g.Deny takes the delivery kind's default: under
// github_pr, no force push, no push to a default branch, no merging or approving a PR;
// under manual, no push to a default branch. g.AllowMergeInto lifts the default's
// gh pr merge deny, since a deny glob can't see a PR's base: the classifier is told which
// bases are allowed, and a glob that matches a default branch is an error. Branch
// protection is then what keeps a worker's merge off the default branch, so the grant
// fails closed: an error unless w.Protected (bees-12.13).
func Build(d config.Delivery, g config.Guards, w Worker) (Rules, error) {
	if len(g.AllowMergeInto) > 0 && d.Kind == "manual" {
		return Rules{}, fmt.Errorf("guards.allow_merge_into: delivery.kind manual has no pull requests to merge")
	}
	if len(g.AllowMergeInto) > 0 && !w.Protected {
		return Rules{}, fmt.Errorf("guards.allow_merge_into: GitHub doesn't confirm the repo's default branch is protected, and with gh pr merge allowed only that protection blocks a worker's merge into it; protect it (Settings → Branches), or drop allow_merge_into")
	}
	for _, glob := range g.Deny {
		if glob == "" || strings.ContainsFunc(glob, unicode.IsControl) {
			return Rules{}, fmt.Errorf("guards.deny %q: a pattern is one non-empty line", glob)
		}
	}
	for _, glob := range g.AllowMergeInto {
		for _, b := range w.DefaultBranches {
			if ok, err := path.Match(glob, b); err != nil {
				return Rules{}, fmt.Errorf("guards.allow_merge_into %q: %w", glob, err)
			} else if ok {
				return Rules{}, fmt.Errorf("guards.allow_merge_into %q matches the default branch %s, which a worker may never merge into", glob, b)
			}
		}
	}
	if w.Dir != "" { // kind none: no worktree or branch to describe or grant, only the denies
		if len(g.AllowMergeInto) > 0 {
			return Rules{}, fmt.Errorf("guards.allow_merge_into: workspace.kind none gives a worker no branch, so no pull request of its own to merge")
		}
		r := Rules{Deny: slices.Clone(g.Deny), Environment: []string{fmt.Sprintf("Host containment: a bees worker, working in the directory %s for the repository %s.", w.Dir, w.Repo)}, Allow: []string{
			fmt.Sprintf("Running this repository's own build, lint and test commands inside the directory %s.", w.Dir),
			"Running bs and bd, the bees tools, to read and send its project's messages and to read and update its own beads.",
		}}
		if len(r.Deny) == 0 {
			r.Deny = pushes(w.DefaultBranches, d.Kind != "manual")
		}
		return r, nil
	}
	env := fmt.Sprintf("Host containment: a bees worker, working in its own git worktree %s on its own branch %s of the repository %s.", w.Worktree, w.Branch, w.Repo)
	if w.Protected && d.Kind != "manual" {
		env += " GitHub protects the repository's default branch, so the worker's work reaches it only through a pull request the keeper merges."
	}
	r := Rules{Deny: slices.Clone(g.Deny), Environment: []string{env}, Allow: []string{
		fmt.Sprintf("Running this repository's own build, lint and test commands (such as go vet, go test, go build -o into the worktree, pre-commit) inside the worktree %s.", w.Worktree),
	}}
	switch d.Kind {
	case "manual":
		if len(r.Deny) == 0 {
			r.Deny = pushes(w.DefaultBranches, false)
		}
		r.Allow = append(r.Allow, fmt.Sprintf("Committing to and pushing (never force-pushing) the worker's own branch %s to the repository's origin.", w.Branch))
	default: // github_pr
		if len(r.Deny) == 0 {
			r.Deny = pushes(w.DefaultBranches, true)
			if len(g.AllowMergeInto) == 0 {
				r.Deny = append(r.Deny, "gh pr merge*", "gh * pr merge*")
			}
			r.Deny = append(r.Deny, "gh pr review* --approve*", "gh pr review* -a*", "gh api *merge*")
		}
		r.Allow = append(r.Allow, fmt.Sprintf("Committing to and pushing (never force-pushing) the worker's own branch %s to the repository's origin, opening a pull request from it, and pushing further commits to that open pull request.", w.Branch))
		if len(g.AllowMergeInto) > 0 {
			r.Allow = append(r.Allow, fmt.Sprintf("Merging the worker's own pull request (gh pr merge) when its base branch matches %s, after checking the base with gh pr view <n> --json baseRefName; never a pull request into %s.", strings.Join(g.AllowMergeInto, " or "), strings.Join(w.DefaultBranches, " or ")))
		}
	}
	r.Allow = append(r.Allow, "Running bs and bd, the bees tools, to read and send its project's messages and to read and update its own beads.")
	return r, nil
}

// Note is the line both role prompts get when g grants merges under github_pr, since the
// delivery fragment otherwise says a worker never merges; "" when it grants none.
func Note(d config.Delivery, g config.Guards) string {
	if len(g.AllowMergeInto) == 0 || d.Kind == "manual" {
		return ""
	}
	return fmt.Sprintf("\nThe keeper's config (guards.allow_merge_into) lets a worker merge its own PR when the PR's base branch matches `%s`: check it with `gh pr view <n> --json baseRefName` first. A PR into the default branch is still never merged by a worker.\n", strings.Join(g.AllowMergeInto, "` or `"))
}

// pushes denies pushes naming one of branches, and with force, force pushes. A git may
// be spelled git, command git or a path (*/git), and its push may follow options
// (git -C dir push, git -c k=v push). Not git * push: that would deny a commit whose
// message says "push … main".
func pushes(branches []string, force bool) []string {
	var bad []string
	if force {
		bad = []string{"--force*", "-f*", "+*"}
	}
	for _, b := range branches {
		bad = append(bad, b+"*")
	}
	for _, b := range branches {
		bad = append(bad, "*:"+b+"*")
	}
	for _, b := range branches {
		bad = append(bad, "*refs/heads/"+b+"*")
	}
	var deny []string
	for _, git := range []string{"git", "command git", "*/git"} {
		for _, push := range []string{git + " push", git + " -* push"} {
			for _, b := range bad {
				deny = append(deny, push+" "+b)
				if !strings.HasPrefix(b, "*") { // a leading * already covers the words before it
					deny = append(deny, push+" * "+b)
				}
			}
		}
	}
	return deny
}
