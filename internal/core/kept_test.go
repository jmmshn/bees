package core

import (
	"context"
	"errors"
	"testing"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/port"
)

// gitRun answers every git call with err.
type gitRun struct{ err error }

func (g gitRun) Run(context.Context, string, ...string) ([]byte, error) { return nil, g.err }

// kept reads a git that ran and said no as "not kept", and a git that couldn't run as an
// error, never "not kept": that would be the refusal with no way on (bees-13.9 review S8).
func TestKeptTellsNoFromCouldntLook(t *testing.T) {
	dir := t.TempDir()
	ended := &logbook.Spawn{Workspace: logbook.Workspace{Kind: config.GitWorktree, Dir: dir, Branch: "toy-1"}}
	c := &Core{Run: gitRun{err: &port.ExitError{Code: 128}}}
	if ok, err := c.kept(context.Background(), ended, dir); ok || err != nil {
		t.Fatalf("git exited 128: kept %v, %v", ok, err)
	}
	c.Run = gitRun{err: errors.New("git: no answer within 60s")}
	if ok, err := c.kept(context.Background(), ended, dir); ok || err == nil {
		t.Fatalf("git couldn't run: kept %v, %v", ok, err)
	}
}
