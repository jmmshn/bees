// Command bsx is the ADR-0010 rebuild of bs, built beside it (go build ./cmd/bsx) and
// never by go install ./cmd/bs. Until the swap (bees-13.9) it refuses the default config
// dir: it runs only on a herdr --session server, on that session's config and logbooks.
package main

import (
	"context"
	"os"
	"path/filepath"
	"syscall"

	"github.com/jmmshn/bees/cmd/bsx/internal/cli"
	"github.com/jmmshn/bees/cmd/bsx/internal/harness/claude"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

func main() {
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	ports := cli.Ports{
		Run:   port.Exec{},
		Proc:  proc.System{},
		Beads: port.BeadsCLI{R: port.Exec{}},
		NewHarness: func(name, model string) port.Harness {
			if name == claude.Name {
				return claude.Adapter{Model: model}
			}
			return nil
		},
		CacheDir: filepath.Join(cache, "bees", "bsx"),
		Exec:     execHarness,
	}
	dir, _ := os.Getwd() // "" when gone: bs then knows the project only from BS_PROJECT
	env := cli.Env{Vars: os.Environ(), Dir: dir, Self: os.Getpid(), Stdin: os.Stdin}
	os.Exit(cli.Run(context.Background(), env, ports, os.Args[1:], os.Stdout, os.Stderr))
}

// execHarness replaces bsx with argv in dir: the queen keeps bsx's pid and start time.
func execHarness(dir string, env, argv []string) error {
	if err := os.Chdir(dir); err != nil {
		return err
	}
	return syscall.Exec(argv[0], argv, env)
}
