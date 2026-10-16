// Package prompt holds the system prompt bs appends to every queen and bee it
// launches. The text lives in system.md, tracked in the repo (R32).
package prompt

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed system.md
var system string

// System is system.md with {{project}} filled in.
func System(project string) string {
	return strings.ReplaceAll(system, "{{project}}", project)
}

// File writes System(project) to <user cache dir>/bees/<project>-system.md and returns
// its path, for claude --append-system-prompt-file. Queen and bees both launch with the
// file, because herdr refuses agent arguments containing newlines (bee.CheckAgentArgs).
// It is rewritten at every launch, through a rename so a concurrent launch never reads
// half a file, and Claude reads it only at startup.
func File(project string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "bees")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, project+"-system-*.md")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.WriteString(System(project)); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	path := filepath.Join(dir, project+"-system.md")
	return path, os.Rename(tmp.Name(), path)
}
