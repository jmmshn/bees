// Command pns lets Claude Code sessions message each other through a shared logbook.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jmmshn/peons/internal/project"
)

const usage = `usage: pns <command> [arguments]

commands:
  init      create the logbook file and table if missing
  chief     start this project's chief session
  spawn     start a peon for a bead in its own herdr workspace
  end       remove a peon's workspace
  epic      create the project's next epic (<project>-<n>)
  send      store a message for a session, then ring it
  inbox     print your unread messages and record receipts
  status    list running sessions and unread counts (--json for scripts)
`

// commands maps each implemented subcommand to its handler.
var commands = map[string]func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error{
	"init":   cmdInit,
	"send":   cmdSend,
	"inbox":  cmdInbox,
	"chief":  cmdChief,
	"spawn":  cmdSpawn,
	"end":    cmdEnd,
	"epic":   cmdEpic,
	"status": cmdStatus,
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if cmd, ok := commands[args[0]]; ok {
		switch err := cmd(context.Background(), args[1:], stdin, stdout, stderr); {
		case errors.Is(err, errHelp):
			return 0
		case errors.Is(err, errUsage):
			return 2
		case errors.Is(err, project.ErrNoSession), errors.Is(err, project.ErrNoProject):
			fmt.Fprintf(stderr, "pns %s: %v\n", args[0], err)
			return 2
		case err != nil:
			fmt.Fprintf(stderr, "pns %s: %v\n", args[0], err)
			return 1
		}
		return 0
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "pns: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
