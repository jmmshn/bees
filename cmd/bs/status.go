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
	"strings"
	"text/tabwriter"

	"github.com/jmmshn/bees/internal/worker"
)

// sessionStatus is one row of bs status, and one object of bs status --json.
type sessionStatus struct {
	Session string `json:"session"`
	Label   string `json:"label,omitempty"` // a worker's herdr workspace label, from its latest spawn
	Running bool   `json:"running"`
	Unread  int    `json:"unread"`
	Error   string `json:"error,omitempty"` // why bs couldn't tell whether it runs; running is then false
}

func cmdStatus(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
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
	p, lb, err := scope(ctx, "status", *proj, "", nil, stderr)
	if err != nil {
		return err
	}
	defer lb.Close()
	counts, err := lb.UnreadCounts(ctx)
	if err != nil {
		return err
	}
	// Liveness is looked up now, from the processes the queen and start entries recorded,
	// and never stored (ADR-0004, ADR-0009); herdr isn't asked. A session whose liveness
	// can't be told is shown as unknown, and bs status exits 1 (motif 11).
	ps := newProcs()
	rows := []sessionStatus{}
	unknown := false
	var unrecorded []string
	for _, name := range slices.Sorted(maps.Keys(counts)) {
		sp, spawned, err := lb.LatestSpawn(ctx, name)
		if err != nil {
			return err
		}
		running, known := false, true
		switch {
		case name == p.Queen():
			running, known, err = worker.QueenAlive(ctx, lb, ps, name)
		case spawned && !sp.Ended:
			running, known, _, err = worker.SpawnAlive(ctx, lb, ps, sp)
		}
		if !known && (name == p.Queen() || spawned && !sp.Ended) {
			unrecorded = append(unrecorded, name)
		}
		r := sessionStatus{Session: name, Label: sp.Label, Running: running, Unread: counts[name]}
		if err != nil {
			r.Error, unknown = err.Error(), true
			fmt.Fprintf(stderr, "bs status: %s: can't tell whether it runs: %v\n", name, err)
		}
		// History is a session that ended (or was never spawned) with nothing waiting. A worker
		// spawned and not ended always shows, and so does a recorded queen, so one that died
		// reads as not running, to restart or end (bees-12.37).
		open := spawned && !sp.Ended || name == p.Queen() && known
		if running || counts[name] > 0 || r.Error != "" || open {
			rows = append(rows, r)
		}
	}
	if len(unrecorded) > 0 {
		fmt.Fprintf(stderr, "bs status: no process recorded for %s, so not running: its start was never confirmed, or a bs from before start entries started it\n", strings.Join(unrecorded, ", "))
	}
	if unknown {
		defer func() { err = cmp.Or(err, error(exitCode(1))) }()
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
		switch {
		case r.Error != "":
			state = "unknown"
		case r.Running:
			state = "running"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", r.Session, cmp.Or(r.Label, "-"), state, r.Unread)
	}
	return tw.Flush()
}
