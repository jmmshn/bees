// Package guards builds the rules a worker launches under (ADR-0009): commands it may
// not run, and prose for a harness's own permission classifier. The core builds them;
// each harness renders them in its own syntax.
package guards

import (
	"fmt"
	"strings"
)

// Rules are one launch's guards.
type Rules struct {
	Deny        []string // shell-glob patterns over a command line; * matches any text, spaces and slashes too
	Environment []string // prose: where the session runs and what it owns
	Allow       []string // prose: what it may do there
}

// Worker is the rules of a worker in its own git worktree dir on its own branch of repo,
// or with branch "" (workspace kind none) in the directory dir.
// A worker owns its worktree and branch, so it may build, test and push its own branch
// (bees-1.51). The environment says the keeper merges by hand only when GitHub reports the
// default branch protected: unchecked, that claim would relax a classifier on pushes
// (bees-7.4).
//
// git -* push covers git -C dir push and git -c k=v push, and */git push covers
// /usr/bin/git push. They are a speed bump, not the guard: a shell finds other spellings,
// and branch protection on main is the real one (docs/SETUP.md). Not git * push: that
// would deny a commit whose message says "push … main".
func Worker(dir, branch, repo string, protected bool) Rules {
	var deny []string
	for _, git := range []string{"git", "command git", "*/git"} {
		for _, push := range []string{git + " push", git + " -* push"} {
			for _, bad := range []string{"--force*", "-f*", "+*", "main*", "master*", "*:main*", "*:master*", "*refs/heads/main*", "*refs/heads/master*"} {
				deny = append(deny, push+" "+bad)
				if !strings.HasPrefix(bad, "*") { // a leading * already covers the words before it
					deny = append(deny, push+" * "+bad)
				}
			}
		}
	}
	deny = append(deny, "gh pr merge*", "gh * pr merge*", "gh pr review* --approve*", "gh pr review* -a*", "gh api *merge*")
	if branch == "" { // workspace kind none: no worktree and no branch, only the directory
		return Rules{
			Deny:        deny,
			Environment: []string{fmt.Sprintf("Host containment: a bees worker, working in the directory %s for the repository %s.", dir, repo)},
			Allow: []string{
				fmt.Sprintf("Running this repository's own build, lint and test commands inside the directory %s.", dir),
				"Running bs and bd, the bees tools, to read and send its project's messages and to read and update its own beads.",
			},
		}
	}
	env := fmt.Sprintf("Host containment: a bees worker, working in its own git worktree %s on its own branch %s of the repository %s.", dir, branch, repo)
	if protected {
		env += " GitHub protects the repository's default branch, so the worker's work reaches it only through a pull request the keeper merges."
	}
	return Rules{
		Deny:        deny,
		Environment: []string{env},
		Allow: []string{
			fmt.Sprintf("Running this repository's own build, lint and test commands (such as go vet, go test, go build -o into the worktree, pre-commit) inside the worktree %s.", dir),
			fmt.Sprintf("Committing to and pushing (never force-pushing) the worker's own branch %s to the repository's origin, opening a pull request from it, and pushing further commits to that open pull request.", branch),
			"Running bs and bd, the bees tools, to read and send its project's messages and to read and update its own beads.",
		},
	}
}
