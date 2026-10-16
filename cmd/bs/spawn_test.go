package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/bee"
)

// fakeHive is herdr, Claude's registry and bd for cmd tests: every bead exists, and
// every start comes up.
type fakeHive struct {
	mu     sync.Mutex
	next   int
	labels map[string]string // workspace label → bee
	live   map[string]bool
}

func (h *fakeHive) CreateWorktree(ctx context.Context, repo, branch, path, label string) (bee.Workspace, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	h.labels[label] = branch
	return bee.Workspace{ID: fmt.Sprintf("w%d", h.next), Label: label, Pane: fmt.Sprintf("w%d:p1", h.next), Path: path}, nil
}

func (h *fakeHive) StartClaude(ctx context.Context, pane, name string, args ...string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.live[name] = true
	return bee.CheckAgentArgs(args)
}

func (h *fakeHive) RemoveWorktree(ctx context.Context, workspace string) error { return nil }
func (h *fakeHive) Workspaces(ctx context.Context) ([]bee.Workspace, error)    { return nil, nil }
func (h *fakeHive) Show(ctx context.Context, id string) (bee.Bead, error) {
	return bee.Bead{ID: id, Title: "t"}, nil
}

func (h *fakeHive) Alive(ctx context.Context, name string, wait time.Duration) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.live[name], nil
}

func useFakeHive(t *testing.T) *fakeHive {
	t.Helper()
	h := &fakeHive{labels: map[string]string{}, live: map[string]bool{}}
	saved := newFleet
	t.Cleanup(func() { newFleet = saved })
	newFleet = func(f bee.Fleet) bee.Fleet {
		f.H, f.S, f.B = h, h, h
		return f
	}
	return h
}

func TestSpawnManyBeadsEachWithItsLabel(t *testing.T) {
	useTempLogbook(t)
	h := useFakeHive(t)
	out, stderr, code := bs(t, "spawn", "--project", "test", "--as", "queen-test",
		"--label", "sort fruits", "test-1.1", "--label", "dedupe todo", "test-1.2")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if h.labels["sort fruits"] != "test-1-1" || h.labels["dedupe todo"] != "test-1-2" {
		t.Fatalf("labels = %v", h.labels)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "spawned test-1-1 (sort fruits) for test-1.1") || !strings.HasPrefix(lines[1], "spawned test-1-2 (dedupe todo)") {
		t.Fatalf("out = %q, want one line per bead in order", out)
	}
	if !strings.HasSuffix(lines[0], "s)") {
		t.Fatalf("out = %q, want the time each spawn took", out)
	}
}

func TestSpawnManyReportsEachFailureAndStartsTheRest(t *testing.T) {
	useTempLogbook(t)
	useFakeHive(t)
	// test-1.2 has no label: refused with the label rules, while test-1.1 still starts.
	out, stderr, code := bs(t, "spawn", "--project", "test", "--as", "queen-test", "--label", "sort fruits", "test-1.1", "test-1.2")
	if code != 2 || !strings.Contains(out, "spawned test-1-1") || !strings.Contains(stderr, "bs spawn: test-1.2: test-1-2: a first spawn needs a label") || !strings.Contains(stderr, "squash merge end") {
		t.Fatalf("exit %d, out %q, stderr %q", code, out, stderr)
	}
}

func TestSpawnTrailingLabelIsUsage(t *testing.T) {
	useTempLogbook(t)
	h := useFakeHive(t)
	_, stderr, code := bs(t, "spawn", "--project", "test", "--as", "queen-test", "--label", "sort fruits", "test-1.1", "--label", "dedupe todo")
	if code != 2 || !strings.Contains(stderr, "usage: bs spawn") || len(h.labels) != 0 {
		t.Fatalf("exit %d, stderr %q, spawned %v; want usage and nothing spawned", code, stderr, h.labels)
	}
}
