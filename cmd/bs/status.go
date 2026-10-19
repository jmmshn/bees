package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"text/tabwriter"

	"github.com/jmmshn/bees/internal/harness/claude"
)

// sessionStatus is one row of bs status, and one object of bs status --json.
type sessionStatus struct {
	Session string `json:"session"`
	Label   string `json:"label,omitempty"` // a worker's herdr workspace label, from its latest spawn
	Running bool   `json:"running"`
	Unread  int    `json:"unread"`
}

func cmdStatus(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project (default: BS_PROJECT, else the current git repo)")
	asJSON := fs.Bool("json", false, `print [{"session": "...", "label": "...", "running": true, "unread": 0}, ...], sorted by session`)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "usage: bs status [--json]")
		return errUsage
	}
	_, lb, err := scope(ctx, *proj, nil)
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
			sp, _, err := lb.LatestSpawn(ctx, name)
			if err != nil {
				return err
			}
			rows = append(rows, sessionStatus{Session: name, Label: sp.Label, Running: running, Unread: counts[name]})
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
	fmt.Fprintln(tw, "SESSION\tLABEL\tSTATE\tUNREAD")
	for _, r := range rows {
		state := "not running"
		if r.Running {
			state = "running"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", r.Session, cmp.Or(r.Label, "-"), state, r.Unread)
	}
	return tw.Flush()
}
