// Command bs runs a fleet of agent sessions over a shared logbook (ADR-0010): a queen per
// project hands beads to workers, each with a goal fixed at spawn.
package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
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
				return agy.Adapter{Model: o.Model, Bin: o.Bin, Bs: exe, Settings: o.Settings, Cache: cacheDir} // its hooks run this bs
			}
			return nil
		},
		CacheDir: cacheDir,
		Exec:     execHarness,
		Revision: revision(),
		Exe:      exe,
	}
	dir, _ := os.Getwd() // "" when gone: bs then knows the project only from BS_PROJECT
	env := cli.Env{Vars: os.Environ(), Dir: dir, Self: os.Getpid(), Stdin: os.Stdin}
	os.Exit(cli.Run(context.Background(), env, ports, os.Args[1:], os.Stdout, os.Stderr))
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
