// Package prompt holds the system prompt bs appends to every queen and bee it
// launches. The text lives in system.md, tracked in the repo (R32).
package prompt

import (
	_ "embed"
	"strings"
)

//go:embed system.md
var system string

// System is system.md with {{project}} filled in.
func System(project string) string {
	return strings.ReplaceAll(system, "{{project}}", project)
}
