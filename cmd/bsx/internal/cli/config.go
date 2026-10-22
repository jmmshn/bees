package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jmmshn/bees/cmd/bsx/internal/core"
)

// Config is the skeleton's slice of ADR-0010's config, read from config.toml in the
// session's config dir; the full surface arrives with bees-13.3.
type Config struct {
	Dir     string `toml:"-"` // the config dir it was read from
	Project string `toml:"project"`
	Harness string `toml:"harness"`
	Paths   struct {
		LogbookDir string `toml:"logbook_dir"`
		Repo       string `toml:"repo"`
		Worktrees  string `toml:"worktrees"`
	} `toml:"paths"`
}

// ErrDefaultConfig is bsx run where the shared bs runs: until the swap (bees-13.9) bsx
// works only inside a herdr --session toy, on that session's config and logbooks.
var ErrDefaultConfig = errors.New("bsx refuses the default config dir until the swap (bees-13.9)")

// configDir is the config dir of a caller with env: <config>/bees/sessions/<HERDR_SESSION>
// (ADR-0010 live rig). The default session's dir, <config>/bees, is bs's own: refused.
func configDir(env []string) (string, error) {
	base := core.Getenv(env, "XDG_CONFIG_HOME")
	if base == "" {
		home := core.Getenv(env, "HOME")
		if home == "" {
			return "", errors.New("neither XDG_CONFIG_HOME nor HOME is set")
		}
		base = filepath.Join(home, ".config")
	}
	session := core.Getenv(env, "HERDR_SESSION")
	if session == "" || session == "default" {
		return "", fmt.Errorf("%w: run it on a herdr --session server, whose config lives in %s",
			ErrDefaultConfig, filepath.Join(base, "bees", "sessions", "<session>"))
	}
	if strings.ContainsAny(session, "/\x00") || session == "." || session == ".." {
		return "", fmt.Errorf("HERDR_SESSION %q can't name a config dir", session)
	}
	return filepath.Join(base, "bees", "sessions", session), nil
}

// loadConfig reads env's config. A missing config dir is refused: a toy's session must be
// set up before bsx writes anything (ADR-0010). The default logbook dir is refused too,
// so no toy ever migrates the real logbooks before the swap.
func loadConfig(env []string) (Config, error) {
	dir, err := configDir(env)
	if err != nil {
		return Config{}, err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return Config{}, fmt.Errorf("no config dir %s for this herdr session: make it, with a config.toml, first", dir)
	}
	var c Config
	if _, err := toml.DecodeFile(filepath.Join(dir, "config.toml"), &c); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", filepath.Join(dir, "config.toml"), err)
	}
	c.Dir = dir
	if c.Paths.LogbookDir == "" {
		return Config{}, fmt.Errorf("%s: paths.logbook_dir is required until the swap (bees-13.9)", filepath.Join(dir, "config.toml"))
	}
	for _, real := range realLogbookDirs(env) {
		if samePath(c.Paths.LogbookDir, real) {
			return Config{}, fmt.Errorf("%w: paths.logbook_dir is the real logbooks' dir, %s", ErrDefaultConfig, c.Paths.LogbookDir)
		}
	}
	if c.Project == "" && c.Paths.Repo != "" {
		c.Project = projectName(filepath.Base(c.Paths.Repo))
	}
	if c.Project == "" {
		return Config{}, fmt.Errorf("%s: set project or paths.repo", filepath.Join(dir, "config.toml"))
	}
	if c.Paths.Worktrees == "" && c.Paths.Repo != "" {
		c.Paths.Worktrees = c.Paths.Repo + "-WT"
	}
	if c.Harness == "" {
		c.Harness = "claude_code"
	}
	return c, nil
}

// projectName is a repo's project name: lowercased, - and _ dropped.
func projectName(repo string) string {
	return strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(repo))
}

// realLogbookDirs are where the real logbooks may be: bs's default, ~/.local/share/bees
// (internal/config), and $XDG_DATA_HOME/bees.
func realLogbookDirs(env []string) []string {
	var out []string
	if h := core.Getenv(env, "HOME"); h != "" {
		out = append(out, filepath.Join(h, ".local", "share", "bees"))
	}
	if d := core.Getenv(env, "XDG_DATA_HOME"); d != "" {
		out = append(out, filepath.Join(d, "bees"))
	}
	return out
}

// samePath reports whether a and b name one directory, through symlinks.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
