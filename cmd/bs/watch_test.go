package main

import (
	"errors"
	"os"
	"testing"

	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/project"
)

// TestMain stubs the watcher: no cmd test may exec the test binary as bs watch.
func TestMain(m *testing.M) {
	startWatcher = func(project.Project, string, logbook.ID, string) error { return nil }
	os.Exit(m.Run())
}

func TestTheRealWatcherRefusesToRunFromATestBinary(t *testing.T) {
	if err := execWatcher(project.Project{Name: "test"}, "bee-a", "s1", "queen"); !errors.Is(err, errTestBinary) {
		t.Fatalf("execWatcher = %v, want errTestBinary (it would rerun this suite)", err)
	}
}
