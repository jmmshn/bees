package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"text/tabwriter"

	"github.com/jmmshn/peons/internal/harness/claude"
)

// sessionStatus is one row of pns status, and one object of pns status --json.
type sessionStatus struct {
	Session string `json:"session"`
	Running bool   `json:"running"`
	Unread  int    `json:"unread"`
}

func cmdStatus(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: PNS_PROJECT, else the current git repo)")
	asJSON := fs.Bool("json", false, `print [{"session": "...", "running": true, "unread": 0}, ...], sorted by session`)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "usage: pns status [--json]")
		return errUsage
	}
	_, lb, err := scope(ctx, *proj)
	if err != nil {
		return err
	}
	defer lb.Close()
	counts, err := lb.UnreadCounts(ctx)
	if err != nil {
		return err
	}
	// Liveness is looked up now and never stored (ADR-0004).
	live, err := claude.List()
	if err != nil {
		return err
	}
	rows := []sessionStatus{}
	for _, name := range slices.Sorted(maps.Keys(counts)) {
		running := slices.ContainsFunc(live, func(s claude.Session) bool { return s.Name == name })
		if running || counts[name] > 0 { // a stopped session with nothing waiting is history
			rows = append(rows, sessionStatus{Session: name, Running: running, Unread: counts[name]})
		}
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no running sessions and no unread messages")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tSTATE\tUNREAD")
	for _, r := range rows {
		state := "not running"
		if r.Running {
			state = "running"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\n", r.Session, state, r.Unread)
	}
	return tw.Flush()
}
