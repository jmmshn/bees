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

// WriteFile writes System(project) to <dir>/<project>-system.md and returns its path,
// for --append-system-prompt-file: herdr refuses agent arguments containing newlines.
func WriteFile(dir, project string) (string, error) {
	path := filepath.Join(dir, project+"-system.md")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(System(project)), 0o644)
}
