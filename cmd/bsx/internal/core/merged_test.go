package core

import (
	"os"
	"path/filepath"
	"testing"
)

// busyWith finds a rebase or bisect that started from the branch: the worktree's HEAD is
// detached then, so worktree list doesn't name the branch, but git won't delete it.
func TestBusyWithSeesARebaseOrBisect(t *testing.T) {
	for _, f := range []string{"rebase-merge/head-name", "rebase-apply/head-name", "BISECT_START"} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte("refs/heads/toy-1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !busyWith(dir, "toy-1") || busyWith(dir, "toy-12") {
			t.Errorf("%s: busyWith toy-1 = %v, toy-12 = %v", f, busyWith(dir, "toy-1"), busyWith(dir, "toy-12"))
		}
	}
	if busyWith(t.TempDir(), "toy-1") {
		t.Error("busy in an idle worktree")
	}
}
