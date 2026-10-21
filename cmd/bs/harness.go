package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/harness/agy"
	"github.com/jmmshn/bees/internal/harness/claude"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/proc"
	"github.com/jmmshn/bees/internal/worker"
)

// harnessFor maps a recorded harness name to its adapter, one forward function. Kind and
// Ring need no config table, so bs send builds it without reading the config files
// (ADR-0008's skip list). It lives here, not in package harness, which its adapters import.
func harnessFor(name string) (harness.Harness, error) {
	switch name {
	case claude.Name:
		return claude.Adapter{}, nil
	case agy.Name:
		return agy.Adapter{}, nil
	}
	return nil, fmt.Errorf("unknown harness %q", name)
}

// adapter is the configured harness for role, built from that role's table.
func adapter(cfg *config.Loaded, role config.Role) (harness.Harness, error) {
	switch cfg.Harness {
	case claude.Name:
		return claude.New(role.ClaudeCode), nil
	case agy.Name:
		return agy.New(role.Agy), nil
	}
	return nil, fmt.Errorf("unknown harness %q", cfg.Harness)
}

// adapterFor is the adapter of harness name for role: the role's table when name is the
// configured harness, else that harness with no settings (a table for an inactive harness
// is a config error). Only ending a worker uses another harness's, for its Kind.
func adapterFor(cfg *config.Loaded, role config.Role, name string) (harness.Harness, error) {
	if name == cfg.Harness {
		return adapter(cfg, role)
	}
	other := *cfg
	other.Harness = name
	return adapter(&other, config.Role{})
}

// switched lists the live sessions of lb recorded under another harness than want: the
// queen (unless queen is "") and every open spawn whose recorded process runs. While one does, a start on want
// would put two harnesses in one fleet (ADR-0009: one machine runs one), so bs queen and
// bs spawn refuse (exit 2), naming them.
func switched(ctx context.Context, lb *logbook.Logbook, queen, want string) ([]string, error) {
	var out []string
	ps := newProcs()
	if queen != "" {
		q, ok, err := lb.LatestQueen(ctx, queen)
		if err != nil {
			return nil, err
		}
		if ok && cmp.Or(q.Harness, harness.Default) != want {
			alive, err := ps.Alive(proc.Process{PID: q.PID, Start: q.Start})
			if err != nil {
				return nil, err
			}
			if alive {
				out = append(out, fmt.Sprintf("%s (%s)", q.Name, cmp.Or(q.Harness, harness.Default)))
			}
		}
	}
	live, err := lb.LiveSpawns(ctx)
	if err != nil {
		return nil, err
	}
	for _, sp := range live {
		if sp.Harness == want {
			continue
		}
		alive, _, _, err := worker.SpawnAlive(ctx, lb, ps, sp)
		if err != nil {
			return nil, err
		}
		if alive {
			out = append(out, fmt.Sprintf("%s (%s)", sp.Name, sp.Harness))
		}
	}
	return out, nil
}

// refuseSwitch is switched's refusal for command cmd.
func refuseSwitch(ctx context.Context, cmd string, lb *logbook.Logbook, queen, want string, stderr io.Writer) error {
	others, err := switched(ctx, lb, queen, want)
	if err != nil {
		return fmt.Errorf("can't tell whether sessions on another harness still run: %w", err)
	}
	if len(others) == 0 {
		return nil
	}
	fmt.Fprintf(stderr, "bs %s: the config says harness = %q, but these sessions still run on another: %s; bs end the workers and exit the queen first, or set harness back: one fleet runs one harness\n", cmd, want, strings.Join(others, ", "))
	return exitCode(2)
}
