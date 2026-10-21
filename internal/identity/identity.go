// Package identity finds the session a bs process runs in from its herdr pane (ADR-0009
// amendment 1): HERDR_PANE_ID, then the entry that recorded that pane, accepted only while
// the process it recorded is alive and an ancestor of this bs. bs pins no env for it, and
// it needs no herdr call.
package identity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
)

// Session is the session a recorded pane names.
type Session struct {
	Project string // the logbook's file name without .db
	Name    string
	Logbook string // the logbook that recorded the pane
	Queen   bool
}

// ErrTwoSessions: two entries name this pane, each with a live ancestor process (motif 4).
var ErrTwoSessions = errors.New("two sessions are recorded in this herdr pane")

// Wait is how long Find waits for a start to be recorded: a worker's first bs call can
// come before bs spawn records its start (worker.confirmWait).
var Wait = 20 * time.Second

// Find returns the session pane names, scanning every <dir>/*.db read only. kind maps a
// recorded harness name to the process name it runs as (claude_code → claude). found is
// false when no entry recorded pane, or none whose process is this bs's ancestor: then the
// caller falls back to --project, --as and the env, as outside herdr. A logbook that can't
// be read is an error naming it (fail closed, SEC-2).
//
// When a worker's entry recorded pane but has no live ancestor process yet, and one of
// this bs's ancestors is a process of that entry's harness that no entry recorded and that
// started within Wait, its start is being confirmed: Find looks again until Wait passes.
// An older unrecorded harness (one a bs from before start entries launched) never waits.
func Find(ctx context.Context, dir, pane string, kind func(harness string) string) (s Session, found bool, err error) {
	chain, err := ancestors()
	if err != nil {
		return Session{}, false, fmt.Errorf("finding this bs's ancestors: %w", err)
	}
	deadline := time.Now().Add(Wait)
	for {
		matches, pending, err := scan(ctx, dir, pane, chain, kind)
		switch {
		case err != nil:
			return Session{}, false, err
		case len(matches) == 1:
			return matches[0], true, nil
		case len(matches) > 1:
			var names []string
			for _, m := range matches {
				names = append(names, m.Name+" ("+m.Logbook+")")
			}
			return Session{}, false, fmt.Errorf("pane %s: %w: %s", pane, ErrTwoSessions, strings.Join(names, ", "))
		case !pending || time.Now().After(deadline):
			return Session{}, false, nil
		}
		select {
		case <-ctx.Done():
			return Session{}, false, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// ancestor is a process above this bs, itself included.
type ancestor struct {
	proc.Process
	name  string
	young bool // started within Wait: a start bs spawn may still be confirming
}

// ancestors returns this bs and every process above it, nearest first. A failed lookup
// is an error: a chain cut short could miss the session (fail closed).
func ancestors() ([]ancestor, error) {
	var out []ancestor
	for pid := os.Getpid(); pid > 1; {
		start, err := proc.Start(pid)
		if err != nil {
			return nil, err
		}
		name, err := proc.Name(pid)
		if err != nil {
			return nil, err
		}
		age, err := proc.Age(pid)
		if err != nil {
			return nil, err
		}
		out = append(out, ancestor{proc.Process{PID: pid, Start: start}, name, age < Wait})
		if pid, err = proc.Parent(pid); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// scan reads every logbook in dir for entries recording pane, and returns those whose
// process is in chain. pending reports an entry not yet matched while chain has a process
// of its harness that no entry recorded.
func scan(ctx context.Context, dir, pane string, chain []ancestor, kind func(string) string) (matches []Session, pending bool, err error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	type candidate struct {
		logbook.Recorded
		Session
	}
	var all []candidate
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".db")
		if !ok || e.IsDir() { // <project>.db.pre-1 copies and -wal files don't end in .db
			continue
		}
		path := filepath.Join(dir, e.Name())
		lb, err := logbook.OpenReadOnly(ctx, path)
		if err != nil {
			return nil, false, err
		}
		recs, err := lb.InPane(ctx, pane)
		lb.Close()
		if err != nil {
			return nil, false, fmt.Errorf("logbook %s: %w", path, err)
		}
		for _, r := range recs {
			all = append(all, candidate{r, Session{Project: name, Name: r.Name, Logbook: path, Queen: r.Queen}})
		}
	}
	recorded := func(p proc.Process) bool {
		return slices.ContainsFunc(all, func(c candidate) bool { return c.HasProcess && c.Process == p })
	}
	for _, c := range all {
		if c.HasProcess && slices.ContainsFunc(chain, func(a ancestor) bool { return a.Process == c.Process }) {
			matches = append(matches, c.Session)
			continue
		}
		k := kind(c.Harness)
		pending = pending || !c.Recorded.Queen && slices.ContainsFunc(chain, func(a ancestor) bool { return a.young && a.name == k && !recorded(a.Process) })
	}
	return matches, pending, nil
}
