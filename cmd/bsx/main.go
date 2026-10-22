// Command bsx is the ADR-0010 rebuild of bs, built beside it (go build ./cmd/bsx) and
// never by go install ./cmd/bs. Until the swap (bees-13.9) it refuses the default config
// dir: it runs only on a herdr --session server, on that session's config and logbooks.
package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/jmmshn/bees/cmd/bsx/internal/cli"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

func main() {
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	ports := cli.Ports{
		Run:      port.Exec{},
		Proc:     proc.System{},
		Beads:    port.BeadsCLI{R: port.Exec{}},
		CacheDir: filepath.Join(cache, "bees", "bsx"),
		// Harness: none until the claude_code adapter (bees-13.4); spawn refuses.
	}
	dir, _ := os.Getwd() // "" when gone: bs then knows the project only from BS_PROJECT
	env := cli.Env{Vars: os.Environ(), Dir: dir, Self: os.Getpid(), Stdin: os.Stdin}
	os.Exit(cli.Run(context.Background(), env, ports, os.Args[1:], os.Stdout, os.Stderr))
}
