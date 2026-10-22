// Package guards builds the deny list a worker starts with (ADR-0010 Config): shell globs
// over a whole command line, harness-neutral, each harness rendering them (Claude Code:
// permissions.deny). They default from the workspace; [guards] deny adds to the default,
// replace = true replaces it, and allow_merge_into lifts the merge deny only while the
// review host protects the default branch. They are a speed bump: a shell finds other spellings, and
// branch protection is the real guard.
package guards

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/jmmshn/bees/cmd/bsx/internal/config"
)

// In is what one worker's guards are built from.
type In struct {
	Kind string // the workspace kind: git_worktree, none or command
	// Git: the workspace dir is a git checkout (none and command deny pushes to the
	// default branch only then).
	Git bool
	// DefaultBranches are the names the repo's default branch may have (DefaultBranches):
	// every one is denied.
	DefaultBranches []string
	// Protected is the review host's word that the default branch is protected: only then
	// may allow_merge_into lift the merge deny (bees-12.13, 12.20). The caller asks the
	// host only when g.AllowMergeInto is set.
	Protected bool
	// MergeDeny and ApproveDeny are the review host's globs for landing and approving a
	// change (gh pr merge, gh pr review --approve on GitHub): a git_worktree worker is
	// denied both, and allow_merge_into lifts only MergeDeny. None without a host.
	MergeDeny, ApproveDeny []string
}

// ErrUnprotected is allow_merge_into on a repo whose default branch the review host doesn't
// confirm is protected (bees-12.20).
var ErrUnprotected = errors.New("guards.allow_merge_into: the review host doesn't confirm the repo's default branch is protected, and with merges allowed only that protection keeps a worker's merge off it; protect it (on GitHub: Settings → Branches), or drop allow_merge_into")

// DefaultBranches is main, master and each of heads (origin's HEAD, the main checkout's
// branch; "" when unknown), without repeats.
func DefaultBranches(heads ...string) []string {
	d := []string{"main", "master"}
	for _, h := range heads {
		if h != "" && !slices.Contains(d, h) {
			d = append(d, h)
		}
	}
	return d
}

// Check refuses a [guards] table bs could never build: an empty or multi-line glob, an
// allow_merge_into glob that isn't one or matches a default branch, or a merge grant
// the review host doesn't back with protection. bs queen runs it at start, so a config every
// spawn would refuse fails there (bees-12.20).
func Check(g config.Guards, in In) error {
	var errs []error
	for _, glob := range g.Deny {
		if glob == "" || strings.ContainsFunc(glob, unicode.IsControl) {
			errs = append(errs, fmt.Errorf("guards.deny %q: a pattern is one non-empty line", glob))
		}
	}
	for _, glob := range g.AllowMergeInto {
		for _, b := range in.DefaultBranches {
			if ok, err := path.Match(glob, b); err != nil {
				errs = append(errs, fmt.Errorf("guards.allow_merge_into %q: %w", glob, err))
				break
			} else if ok {
				errs = append(errs, fmt.Errorf("guards.allow_merge_into %q matches the default branch %s, which a worker may never merge into", glob, b))
				break
			}
		}
	}
	if len(g.AllowMergeInto) > 0 && in.Kind != config.GitWorktree {
		errs = append(errs, fmt.Errorf("guards.allow_merge_into: a %s workspace gives a worker no branch of its own to merge", in.Kind))
	} else if len(g.AllowMergeInto) > 0 && !in.Protected {
		errs = append(errs, ErrUnprotected)
	}
	return errors.Join(errs...)
}

// Build returns the deny globs of a worker in.
func Build(g config.Guards, in In) ([]string, error) {
	if err := Check(g, in); err != nil {
		return nil, err
	}
	if g.Replace {
		return slices.Clone(g.Deny), nil
	}
	var deny []string
	switch {
	case in.Kind == config.GitWorktree:
		deny = append(pushes(in.DefaultBranches), force()...)
		if len(g.AllowMergeInto) == 0 {
			deny = append(deny, in.MergeDeny...)
		}
		deny = append(deny, in.ApproveDeny...) // no grant lifts an approval
	case in.Git: // none and command in a git checkout (bees-12.21: no kind allows a force push)
		deny = append(pushes(in.DefaultBranches), force()...)
	}
	for _, d := range g.Deny {
		if !slices.Contains(deny, d) {
			deny = append(deny, d)
		}
	}
	return deny, nil
}

// Denies returns the first glob of deny that matches the whole command line cmd, where *
// matches any text, spaces and slashes too, and nothing else is special: the matching
// Claude Code's Bash(...) rules promise (probed on 2.1.302), and the oracle of the guard
// tests.
func Denies(deny []string, cmd string) (string, bool) {
	for _, glob := range deny {
		if match(glob, cmd) {
			return glob, true
		}
	}
	return "", false
}

// match is glob over s: each piece between *s must appear in order, the first at the
// start and the last at the end.
func match(glob, s string) bool {
	parts := strings.Split(glob, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := len(parts) - 1
	if last == 0 {
		return s == ""
	}
	for _, part := range parts[1:last] {
		i := strings.Index(s, part)
		if i < 0 {
			return false
		}
		s = s[i+len(part):]
	}
	return strings.HasSuffix(s, parts[last])
}

// gitPushes are the spellings of git push a glob starts with: git, command git or a path
// (*/git), with options before push (git -C dir push, git -c k=v push). Not git * push:
// that would deny a commit whose message says "push … main".
func gitPushes() []string {
	var out []string
	for _, git := range []string{"git", "command git", "*/git"} {
		out = append(out, git+" push", git+" -* push")
	}
	return out
}

// word is the globs of push followed by w as a whole word: last on the line, or followed
// by a space. Never w* (bees-12.21: main* denied a push of maintenance).
func word(push, w string) []string {
	return []string{push + " " + w, push + " " + w + " *", push + " * " + w, push + " * " + w + " *"}
}

// pushes denies a push naming one of branches as its destination: the branch alone, a
// refspec's destination (x:main), or refs/heads/main.
func pushes(branches []string) []string {
	var deny []string
	for _, push := range gitPushes() {
		for _, b := range branches {
			for _, w := range []string{b, "*:" + b, "*:refs/heads/" + b, "refs/heads/" + b} {
				deny = append(deny, word(push, w)...)
			}
		}
	}
	return compact(deny)
}

// force denies a force push: --force (and --force-with-lease), -f, or a + refspec, for
// every workspace that denies anything (bees-12.21: manual allowed it).
func force() []string {
	var deny []string
	for _, push := range gitPushes() {
		deny = append(deny, push+" --force*", push+" * --force*")
		deny = append(deny, word(push, "-f")...)
		deny = append(deny, push+" +*", push+" * +*")
	}
	return compact(deny)
}

// compact drops a glob another already covers: "x * *:main" duplicates "x *:main" once a
// leading * has eaten the words before it.
func compact(globs []string) []string {
	var out []string
	for _, g := range globs {
		if !slices.Contains(out, g) && !strings.Contains(g, " * *") {
			out = append(out, g)
		}
	}
	return out
}
