// Command pns lets Claude Code sessions message each other through a shared logbook.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

const usage = `usage: pns <command> [arguments]

commands:
  init      create the logbook file and table if missing
  send      store a message for a session, then ring it
  inbox     print your unread messages and record receipts
  status    list sessions and their unread counts
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "init", "send", "inbox":
		cmds := map[string]func(context.Context, []string, io.Writer, io.Writer) error{
			"init": cmdInit, "send": cmdSend, "inbox": cmdInbox,
		}
		switch err := cmds[args[0]](context.Background(), args[1:], stdout, stderr); {
		case errors.Is(err, errUsage):
			return 2
		case err != nil:
			fmt.Fprintf(stderr, "pns %s: %v\n", args[0], err)
			return 1
		}
		return 0
	case "status":
		fmt.Fprintf(stderr, "pns: %s is not implemented yet\n", args[0])
		return 2
	default:
		fmt.Fprintf(stderr, "pns: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
