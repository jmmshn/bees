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

// Rules are one worker's guards.
type Rules struct {
	// Deny holds shell globs over a whole command line, where * matches any text, spaces
	// and slashes too, and nothing else is special.
	Deny []string
	// Allow holds intents in prose, naming the worker's branch and worktree, for a
	// harness that judges commands by intent.
	Allow []string
}

// Worker names what one worker's rules are about.
type Worker struct {
	Branch, Worktree string
	// Defaults are the repo's default branch names: main and master, plus origin's HEAD.
	// Every one is denied, so a repo whose default is develop guards develop (bees-11.7).
	Defaults []string
}

// Defaults is the list Worker.Defaults wants: main, master and head (origin's default
// branch, "" when unknown), without repeats.
func Defaults(head string) []string {
	d := []string{"main", "master"}
	if head != "" && !slices.Contains(d, head) {
		d = append(d, head)
	}
	return d
}

// Build returns w's rules. An empty g.Deny takes the delivery kind's default: under
// github_pr, no force push, no push to a default branch, no merging or approving a PR;
// under manual, no push to a default branch. g.AllowMergeInto lifts the default's
// gh pr merge deny for pull requests into those base branches; a glob that matches a
// default branch is an error, so the default branch stays denied.
func Build(d config.Delivery, g config.Guards, w Worker) (Rules, error) {
	for _, glob := range g.Deny {
		if glob == "" || strings.ContainsFunc(glob, unicode.IsControl) {
			return Rules{}, fmt.Errorf("guards.deny %q: a pattern is one non-empty line", glob)
		}
	}
	for _, glob := range g.AllowMergeInto {
		for _, b := range w.Defaults {
			if ok, err := path.Match(glob, b); err != nil {
				return Rules{}, fmt.Errorf("guards.allow_merge_into %q: %w", glob, err)
			} else if ok {
				return Rules{}, fmt.Errorf("guards.allow_merge_into %q matches the default branch %s, which stays denied", glob, b)
			}
		}
	}
	r := Rules{Deny: slices.Clone(g.Deny), Allow: []string{
		fmt.Sprintf("Running this repository's own build, lint and test commands (such as go vet, go test, go build -o into the worktree, pre-commit) inside the worktree %s.", w.Worktree),
	}}
	switch d.Kind {
	case "manual":
		if len(r.Deny) == 0 {
			r.Deny = pushes(w.Defaults, false)
		}
		r.Allow = append(r.Allow, fmt.Sprintf("Committing to and pushing (never force-pushing) the worker's own branch %s to the repository's origin.", w.Branch))
	default: // github_pr
		if len(r.Deny) == 0 {
			r.Deny = pushes(w.Defaults, true)
			if len(g.AllowMergeInto) == 0 {
				r.Deny = append(r.Deny, "gh pr merge*", "gh * pr merge*")
			}
			r.Deny = append(r.Deny, "gh pr review* --approve*", "gh pr review* -a*", "gh api *merge*")
		}
		r.Allow = append(r.Allow, fmt.Sprintf("Committing to and pushing (never force-pushing) the worker's own branch %s to the repository's origin, opening a pull request from it, and pushing further commits to that open pull request.", w.Branch))
		if len(g.AllowMergeInto) > 0 {
			r.Allow = append(r.Allow, fmt.Sprintf("Merging the worker's own pull request (gh pr merge) when its base branch matches %s, after checking the base with gh pr view <n> --json baseRefName; never a pull request into %s.", strings.Join(g.AllowMergeInto, " or "), strings.Join(w.Defaults, " or ")))
		}
	}
	r.Allow = append(r.Allow, "Running bs and bd, the bees tools, to read and send its project's messages and to read and update its own beads.")
	return r, nil
}

// Note is the line both role prompts get when g grants merges, since the delivery
// fragment otherwise says a worker never merges; "" when it grants none.
func Note(g config.Guards) string {
	if len(g.AllowMergeInto) == 0 {
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
