package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"

	"github.com/jmmshn/bees/internal/prompt"
)

// execClaude replaces bs with claude, run in dir with env. Tests swap it out.
var execClaude = execClaudeProcess

func execClaudeProcess(dir string, env []string, args ...string) error {
	path, err := exec.LookPath("claude")
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	return syscall.Exec(path, append([]string{"claude"}, args...), env)
}

func cmdQueen(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("queen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "usage: bs queen [--project <name>]")
		return errUsage
	}
	p, _, err := resolve("queen", *proj)
	if err == nil {
		err = p.NeedRepo()
	}
	if err != nil {
		return err
	}
	// The queen starts the project, so her bs commands find a logbook.
	_, lb, err := scope(ctx, "queen", *proj, stderr)
	if err != nil {
		return err
	}
	lb.Close()
	// No BS_* variables: the queen's bs finds its name from --name and its project from
	// the repo (R25). Drop inherited ones so they can't override either.
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "BS_SESSION=") || strings.HasPrefix(kv, "BS_PROJECT=")
	})
	file, err := prompt.File(prompt.Queen, p.Name)
	if err != nil {
		return err
	}
	return execClaude(p.Repo, env, "--name", p.Queen(), "--append-system-prompt-file", file)
}
