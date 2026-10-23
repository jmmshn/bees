// Package command is a command workspace's VCS: the keeper's own argv answer the four
// facts the goal checks read (port.VCS), so a VCS bs knows nothing of (a monorepo client
// with CLs, jj, hg) plugs in through config alone (bees-14.4).
//
// Each command runs in the workspace dir through the checks runner (argv only, PATH, HOME
// and LANG, its process group killed at its timeout) and prints one JSON object on stdout;
// keys it doesn't know are ignored, stderr is never read:
//
//	branch   {"tip": "<commit>"}       the workspace's head on branch {branch}; absent, "" or null: none
//	pushed   {"tip": "<commit>"}       what the workspace last uploaded of {branch}; absent, "" or null: can't say
//	has      {"has": true|false}       the workspace has {commit}
//	descends {"descends": true|false}  {commit} is {ancestor} or descends from it
//
// A command that exits non-zero, runs past its timeout, or prints anything else couldn't
// answer: an error, never "no" (Rule G).
package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jmmshn/bees/internal/checks"
	"github.com/jmmshn/bees/internal/port"
)

// Keys are the placeholders a VCS argv may name: the workspace dir, the branch asked
// about (the worker's name), and the commits asked about.
var Keys = []string{"dir", "branch", "commit", "ancestor"}

// Cmds are the four argv, as config gives them and a spawn freezes them.
type Cmds struct {
	Branch   checks.Cmd `json:"branch"`
	Pushed   checks.Cmd `json:"pushed"`
	Has      checks.Cmd `json:"has"`
	Descends checks.Cmd `json:"descends"`
}

// VCS runs Cmds; Getenv is the environment PATH, HOME and LANG come from (nil: bs's own).
type VCS struct {
	Cmds   Cmds
	Getenv func(string) string
}

var _ port.VCS = VCS{}

func (v VCS) Branch(ctx context.Context, dir, name string) (string, bool, error) {
	return v.tip(ctx, "branch", v.Cmds.Branch, dir, map[string]checks.Value{"branch": {S: name}})
}

func (v VCS) Pushed(ctx context.Context, dir, name string) (string, bool, error) {
	return v.tip(ctx, "pushed", v.Cmds.Pushed, dir, map[string]checks.Value{"branch": {S: name}})
}

func (v VCS) Has(ctx context.Context, dir, commit string) (bool, error) {
	return v.yes(ctx, "has", v.Cmds.Has, dir, map[string]checks.Value{"commit": {S: commit}})
}

func (v VCS) Descends(ctx context.Context, dir, ancestor, commit string) (bool, error) {
	return v.yes(ctx, "descends", v.Cmds.Descends, dir, map[string]checks.Value{"ancestor": {S: ancestor}, "commit": {S: commit}})
}

func (v VCS) tip(ctx context.Context, key string, c checks.Cmd, dir string, vals map[string]checks.Value) (string, bool, error) {
	var out struct {
		Tip *string `json:"tip"`
	}
	if err := v.run(ctx, key, c, dir, vals, &out); err != nil {
		return "", false, err
	}
	if out.Tip == nil || *out.Tip == "" {
		return "", false, nil
	}
	return *out.Tip, true, nil
}

// yes reads {key: bool}, which must be there.
func (v VCS) yes(ctx context.Context, key string, c checks.Cmd, dir string, vals map[string]checks.Value) (bool, error) {
	out := map[string]*bool{}
	if err := v.run(ctx, key, c, dir, vals, &out); err != nil {
		return false, err
	}
	b := out[key]
	if b == nil {
		return false, fmt.Errorf("%w: [workspace.command] %s printed no %q: true|false", checks.ErrCantRun, key, key)
	}
	return *b, nil
}

// run runs c in dir and decodes the one JSON object it printed into out.
func (v VCS) run(ctx context.Context, key string, c checks.Cmd, dir string, vals map[string]checks.Value, out any) error {
	vals["dir"] = checks.Value{S: dir}
	r, err := checks.Run(ctx, c, dir, vals, v.Getenv)
	switch {
	case err != nil:
		return fmt.Errorf("[workspace.command] %s: %w", key, err)
	case r.Code != 0:
		return fmt.Errorf("%w: [workspace.command] %s: %s exited %d: %s", checks.ErrCantRun, key, strings.Join(r.Argv, " "), r.Code, strings.TrimSpace(string(r.Output)))
	}
	if err := decode(r.Stdout, out); err != nil {
		return fmt.Errorf("%w: [workspace.command] %s: %s printed %q, not one JSON object: %w", checks.ErrCantRun, key, strings.Join(r.Argv, " "), strings.TrimSpace(string(r.Stdout)), err)
	}
	return nil
}

// decode reads b as exactly one JSON object into out, with a type for each key it reads.
func decode(b []byte, out any) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		return errors.New("want a {…} object")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if err := d.Decode(out); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("more after the object")
	}
	return nil
}
