// Package prompt holds the role prompts bs appends to the sessions it launches:
// QUEEN.md for the queen, WORKER.md for workers, each followed by PITFALLS.md (the
// Known pitfalls lines both roles hit, continuing the role's list), BEADS.md (the
// bead rules both share) and a delivery fragment (how work lands: delivery/<kind>.md,
// or the user's own file; ADR-0009). The text is tracked in the repo (R32, R40).
package prompt

import (
	"embed"
	"fmt"
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
	//go:embed delivery/*.md
	deliveries embed.FS
)

// Delivery is the delivery fragment: the file fragment names when it is set, read now so
// an unreadable one stops the launch, else kind's built-in delivery/<kind>.md.
func Delivery(kind, fragment string) (string, error) {
	if fragment != "" {
		b, err := os.ReadFile(fragment)
		if err != nil {
			return "", fmt.Errorf("delivery.fragment: %w", err)
		}
		return string(b), nil
	}
	b, err := deliveries.ReadFile("delivery/" + kind + ".md")
	if err != nil {
		return "", fmt.Errorf("delivery.kind %q: no built-in fragment", kind)
	}
	return string(b), nil
}

// Text is r's prompt followed by PITFALLS.md, BEADS.md and r's part of the delivery
// fragment under a "## Delivery" heading (the roles point to it), with {{project}} filled
// in. A fragment line "<!-- queen -->" or "<!-- worker -->" starts a part only that role
// reads, up to the next such line; text before the first one goes to both.
func Text(r Role, project, delivery string) string {
	role := worker
	if r == Queen {
		role = queen
	}
	return strings.ReplaceAll(role+pitfalls+"\n"+beads+"\n## Delivery\n\n"+part(r, delivery), "{{project}}", project)
}

// part is r's share of a delivery fragment: the shared lead, then r's own sections.
func part(r Role, delivery string) string {
	var b strings.Builder
	mine := true
	for line := range strings.SplitAfterSeq(delivery, "\n") {
		switch strings.TrimSpace(line) {
		case "<!-- queen -->":
			mine = r == Queen
		case "<!-- worker -->":
			mine = r == Worker
		default:
			if mine {
				b.WriteString(line)
			}
		}
	}
	return b.String()
}

// File writes Text(r, project, delivery) to <user cache dir>/bees/<project>-<role>.md and
// returns its path, for claude --append-system-prompt-file. Queen and workers both launch
// with a file, because herdr refuses agent arguments containing newlines
// (worker.CheckAgentArgs). It is rewritten at every launch, through a rename so a
// concurrent launch never reads half a file, and Claude reads it only at startup.
func File(r Role, project, delivery string) (string, error) {
	return Cache(project+"-"+string(r)+".md", Text(r, project, delivery))
}

// Dir is <cache dir>/bees, where bs keeps the files it launches Claude with. The cache
// dir is XDG_CACHE_HOME on every OS (os.UserCacheDir ignores it on macOS), else
// os.UserCacheDir(); a relative XDG_CACHE_HOME is an error (ADR-0008).
func Dir() (string, error) {
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache != "" && !filepath.IsAbs(cache) {
		return "", fmt.Errorf("XDG_CACHE_HOME=%s is not an absolute path", cache)
	}
	if cache == "" {
		var err error
		if cache, err = os.UserCacheDir(); err != nil {
			return "", err
		}
	}
	dir := filepath.Join(cache, "bees")
	return dir, os.MkdirAll(dir, 0o700) // private, like the logbook
}

// Private writes body to a new file in Dir named by pattern (its * a random number) and
// returns its path. The file is created exclusively and readable only by the user, so no
// other process can have it first or swap it in under a name bs would reuse.
func Private(pattern, body string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Remove deletes the files in Dir matching pattern (filepath.Match syntax).
func Remove(pattern string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(dir, pattern))
	for _, path := range paths {
		if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
			err = rerr
		}
	}
	return err
}

// Cache writes body to <user cache dir>/bees/<name> through a rename, and returns its path.
func Cache(name, body string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, name+"-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	return path, os.Rename(tmp.Name(), path)
}
