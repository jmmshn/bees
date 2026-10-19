// Command bs lets Claude Code sessions message each other through a shared logbook.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jmmshn/bees/internal/project"
)

const usage = `usage: bs <command> [arguments]

commands:
  init      create the logbook file and table if missing
  queen     start this project's queen session
  spawn     start a worker for a bead in its own herdr workspace
  end       remove a worker's workspace; its branch is kept
  prune     delete ended workers' local branches once their work is merged
  epic      create the project's next epic (<project>-<n>)
  bead      list the project's beads (ls) or add one under an epic (new)
  send      store a message for a session, then ring it
  inbox     print your unread messages and record receipts
  status    list running sessions and unread counts (--json for scripts)
  version   print the commit bs was built from
`

// commands maps each implemented subcommand to its handler.
var commands = map[string]func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error{
	"init":    cmdInit,
	"send":    cmdSend,
	"inbox":   cmdInbox,
	"queen":   cmdQueen,
	"spawn":   cmdSpawn,
	"end":     cmdEnd,
	"prune":   cmdPrune,
	"epic":    cmdEpic,
	"bead":    cmdBead,
	"status":  cmdStatus,
	"version": cmdVersion,
}

// exitCode is an error that only sets bs's exit status; the command has printed why.
type exitCode int

func (c exitCode) Error() string { return fmt.Sprintf("exit status %d", int(c)) }

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if cmd, ok := commands[args[0]]; ok {
		warnIfStale(context.Background(), stderr)
		var code exitCode
		switch err := cmd(context.Background(), args[1:], stdin, stdout, stderr); {
		case errors.As(err, &code): // the command already said why
			return int(code)
		case errors.Is(err, errHelp):
			return 0
		case errors.Is(err, errUsage):
			return 2
		case errors.Is(err, project.ErrNoSession), errors.Is(err, project.ErrNoProject), errors.Is(err, project.ErrWrongProject):
			fmt.Fprintf(stderr, "bs %s: %v\n", args[0], err)
			return 2
		case err != nil:
			fmt.Fprintf(stderr, "bs %s: %v\n", args[0], err)
			return 1
		}
		return 0
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "bs: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
