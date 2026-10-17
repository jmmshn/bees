package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/worker"
)

// fakeHive is herdr, Claude's registry and bd for cmd tests: every bead exists, and
// every start comes up.
type fakeHive struct {
	mu     sync.Mutex
	next   int
	labels map[string]string // workspace label → worker
	live   map[string]bool
}

func (h *fakeHive) CreateWorktree(ctx context.Context, repo, branch, path, label string) (worker.Workspace, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	h.labels[label] = branch
	return worker.Workspace{ID: fmt.Sprintf("w%d", h.next), Label: label, Pane: fmt.Sprintf("w%d:p1", h.next), Path: path}, nil
}

func (h *fakeHive) StartClaude(ctx context.Context, pane, name string, args ...string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.live[name] = true
	return worker.CheckAgentArgs(args)
}

func (h *fakeHive) RemoveWorktree(ctx context.Context, workspace string, force bool) error {
	return nil
}
func (h *fakeHive) Workspaces(ctx context.Context) ([]worker.Workspace, error) { return nil, nil }
func (h *fakeHive) Show(ctx context.Context, id string) (worker.Bead, error) {
	if strings.HasSuffix(id, ".404") {
		return worker.Bead{}, fmt.Errorf("no bead %s", id)
	}
	return worker.Bead{ID: id, Title: "t"}, nil
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
	newFleet = func(f worker.Fleet) worker.Fleet {
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

func TestSpawnManyRefusedStartsNothing(t *testing.T) {
	useTempLogbook(t)
	h := useFakeHive(t)
	// test-1.2 has no label: the whole call is refused, with the label rules.
	out, stderr, code := bs(t, "spawn", "--project", "test", "--as", "queen-test", "--label", "sort fruits", "test-1.1", "test-1.2")
	if code != 2 || out != "" || len(h.labels) != 0 || !strings.Contains(stderr, "bs spawn: test-1.1: not started") ||
		!strings.Contains(stderr, "bs spawn: test-1.2: test-1-2: a first spawn needs a label") || !strings.Contains(stderr, "squash merge end") {
		t.Fatalf("exit %d, out %q, stderr %q, spawned %v", code, out, stderr, h.labels)
	}
}

func TestSpawnManyStartsTheRestPastAFailure(t *testing.T) {
	useTempLogbook(t)
	useFakeHive(t)
	out, stderr, code := bs(t, "spawn", "--project", "test", "--as", "queen-test", "--label", "lost bead", "test-1.404", "--label", "sort fruits", "test-1.1")
	if code != 1 || !strings.Contains(out, "spawned test-1-1") || !strings.Contains(stderr, "bs spawn: test-1.404: no bead test-1.404") {
		t.Fatalf("exit %d, out %q, stderr %q", code, out, stderr)
	}
}

func TestSpawnArgumentMistakesAreUsage(t *testing.T) {
	useTempLogbook(t)
	h := useFakeHive(t)
	for _, args := range [][]string{
		{"--label", "sort fruits", "test-1.1", "--label", "dedupe todo"},                             // a label with no bead
		{"--label", "sort fruits", "--label", "dedupe todo", "test-1.1"},                             // two labels for one bead
		{"--label", "sort fruits", "test-1.1", "--repo", "/x", "--label", "dedupe todo", "test-1.2"}, // --repo after a bead
	} {
		_, stderr, code := bs(t, append([]string{"spawn", "--project", "test", "--as", "queen-test"}, args...)...)
		if code != 2 || len(h.labels) != 0 {
			t.Errorf("spawn %q: exit %d, stderr %q, spawned %v; want usage and nothing spawned", args, code, stderr, h.labels)
		}
	}
}

func TestSpawnBeadsAfterDoubleDash(t *testing.T) {
	useTempLogbook(t)
	h := useFakeHive(t)
	if _, stderr, code := bs(t, "spawn", "--project", "test", "--as", "queen-test", "--label", "sort fruits", "--", "test-1.1"); code != 0 || h.labels["sort fruits"] != "test-1-1" {
		t.Fatalf("exit %d, stderr %q, spawned %v", code, stderr, h.labels)
	}
}
