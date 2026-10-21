package main

import (
	"fmt"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/harness/agy"
	"github.com/jmmshn/bees/internal/harness/claude"
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
