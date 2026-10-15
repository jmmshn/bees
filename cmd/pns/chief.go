package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/jmmshn/peons/internal/project"
)

// execClaude replaces pns with claude, run in dir with env. Tests swap it out.
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

func cmdChief(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("chief", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: PNS_PROJECT, else the current git repo)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "usage: pns chief [--project <name>]")
		return errUsage
	}
	p, err := project.Resolve(*proj)
	if err != nil {
		return err
	}
	env := append(os.Environ(), "PNS_PROJECT="+p.Name, "PNS_SESSION="+p.Chief())
	return execClaude(p.Repo, env, "--name", p.Chief())
}
