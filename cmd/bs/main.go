// Command bs runs a fleet of agent sessions over a shared logbook (ADR-0010): a queen per
// project hands beads to workers, each with a goal fixed at spawn.
package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"syscall"

	"github.com/jmmshn/bees/internal/cli"
	"github.com/jmmshn/bees/internal/harness/agy"
	"github.com/jmmshn/bees/internal/harness/claude"
	"github.com/jmmshn/bees/internal/port"
	"github.com/jmmshn/bees/internal/proc"
)

func main() {
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	exe, _ := os.Executable() // "": bs is then checked as the shared install
	cacheDir := filepath.Join(cache, "bees", "bs")
	ports := cli.Ports{
		Run:   port.Exec{},
		Proc:  proc.System{},
		Beads: port.BeadsCLI{R: port.Exec{}},
		NewHarness: func(name string, o cli.HarnessOpts) port.Harness {
			switch name {
			case claude.Name:
				return claude.Adapter{Model: o.Model}
			case agy.Name:
				return agy.Adapter{Model: o.Model, Bin: o.Bin, Bs: exe, Settings: o.Settings, Run: port.Exec{}} // its hooks run this bs; its ring, herdr
			}
			return nil
		},
		CacheDir: cacheDir,
		StartDir: startDir(),
		Exec:     execHarness,
		Revision: revision(),
		Exe:      exe,
	}
	dir, _ := os.Getwd() // "" when gone: bs then knows the project only from BS_PROJECT
	env := cli.Env{Vars: os.Environ(), Dir: dir, Self: os.Getpid(), Stdin: os.Stdin}
	os.Exit(cli.Run(context.Background(), env, ports, os.Args[1:], os.Stdout, os.Stderr))
}

// startDir is where per-start dirs go: agy lists them with the workspace, sorted, the
// first where its commands run, so they live under a root that sorts after typical repos
// (/home, /Users): $XDG_RUNTIME_DIR (/run/user/<uid>), else the temp dir (macOS's
// /var/folders/…), named for the user there, since /tmp is everyone's (bees-13.13.28).
func startDir() string {
	if root := os.Getenv("XDG_RUNTIME_DIR"); root != "" {
		return filepath.Join(root, "bees", "bs")
	}
	return filepath.Join(os.TempDir(), "bees-"+strconv.Itoa(os.Getuid()), "bs")
}

// execHarness replaces bs with argv in dir: the queen keeps bs's pid and start time.
func execHarness(dir string, env, argv []string) error {
	if err := os.Chdir(dir); err != nil {
		return err
	}
	return syscall.Exec(argv[0], argv, env)
}

// revision is the commit bs was built from, from Go's VCS stamp (go install in the repo
// sets it), or "" if there is none.
func revision() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return ""
}
