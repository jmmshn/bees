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

	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/prompt"
)

// execAgent replaces bs with the harness binary program, run in dir with env. Tests swap
// it out.
var execAgent = execAgentProcess

func execAgentProcess(dir string, env []string, program string, args ...string) error {
	path, err := exec.LookPath(program)
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	return syscall.Exec(path, append([]string{program}, args...), env)
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
	p, cfg, err := resolve("queen", *proj, "")
	if err == nil {
		err = p.NeedRepo()
	}
	if err != nil {
		return err
	}
	// The queen starts the project, so her bs commands find a logbook.
	_, lb, err := scope(ctx, "queen", *proj, "", stderr)
	if err != nil {
		return err
	}
	lb.Close()
	// No BS_SESSION or BS_PROJECT: the queen's bs finds its name from --name and its project
	// from the repo (R25). Drop inherited ones so they can't override either. A BS_LOGBOOK
	// passes on absolute: relative, it would name another file in her repo.
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "BS_SESSION=") || strings.HasPrefix(kv, "BS_PROJECT=") || strings.HasPrefix(kv, "BS_LOGBOOK=")
	})
	if os.Getenv("BS_LOGBOOK") != "" {
		env = append(env, "BS_LOGBOOK="+p.Logbook)
	}
	file, err := prompt.File(prompt.Queen, p.Name)
	if err != nil {
		return err
	}
	h, err := adapter(cfg, cfg.Queen)
	if err != nil {
		return err
	}
	l, err := h.Prepare(harness.Start{Role: prompt.Queen, Name: p.Queen(), Dir: p.Repo, PromptFile: file})
	if err != nil {
		return err
	}
	if len(l.Files) > 0 {
		return fmt.Errorf("harness %s wants files for the queen, which bs queen doesn't write yet", cfg.Harness)
	}
	return execAgent(p.Repo, env, h.Kind(), l.Args...)
}
