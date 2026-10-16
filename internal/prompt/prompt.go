// Package prompt holds the role prompts bs appends to the sessions it launches:
// QUEEN.md for the queen, WORKER.md for workers, each followed by PITFALLS.md (the
// Known pitfalls lines both roles hit, continuing the role's list) and BEADS.md (the
// bead rules both share). The text is tracked in the repo (R32, R40).
package prompt

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
)

// Role picks a role prompt; its value names the cache file.
type Role string

const (
	Queen  Role = "queen"
	Worker Role = "worker"
)

var (
	//go:embed QUEEN.md
	queen string
	//go:embed WORKER.md
	worker string
	//go:embed PITFALLS.md
	pitfalls string
	//go:embed BEADS.md
	beads string
)

// Text is r's prompt followed by PITFALLS.md and BEADS.md, with {{project}} filled in.
func Text(r Role, project string) string {
	role := worker
	if r == Queen {
		role = queen
	}
	return strings.ReplaceAll(role+pitfalls+"\n"+beads, "{{project}}", project)
}

// File writes Text(r, project) to <user cache dir>/bees/<project>-<role>.md and returns
// its path, for claude --append-system-prompt-file. Queen and workers both launch with a
// file, because herdr refuses agent arguments containing newlines (worker.CheckAgentArgs).
// It is rewritten at every launch, through a rename so a concurrent launch never reads
// half a file, and Claude reads it only at startup.
func File(r Role, project string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "bees")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := project + "-" + string(r)
	tmp, err := os.CreateTemp(dir, name+"-*.md")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.WriteString(Text(r, project)); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".md")
	return path, os.Rename(tmp.Name(), path)
}
