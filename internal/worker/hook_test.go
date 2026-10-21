package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHookGetsItsEnvAndDirAndReportsItsOutcome(t *testing.T) {
	dir := t.TempDir()
	h, out, err := runHook(context.Background(), "init", `echo "$BS_NAME in $(pwd)"; echo first >&2; echo second >&2`, dir, map[string]string{"BS_NAME": "bees-8"})
	if err != nil || h.Exit != 0 || h.Stderr != "first" {
		t.Fatalf("hook = %+v, %v", h, err)
	}
	if want := "bees-8 in " + dir + "\n"; out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
	h, _, err = runHook(context.Background(), "finalize", `echo "dirty: x" >&2; exit 3`, dir, nil)
	if err == nil || h.Exit != 3 || h.Stderr != "dirty: x" || !strings.Contains(err.Error(), "finalize hook exited 3: dirty: x") {
		t.Fatalf("hook = %+v, %v", h, err)
	}
}

func TestHookIsKilledAsAGroupOnItsTimeout(t *testing.T) {
	defer func(d time.Duration) { hookTimeout = d }(hookTimeout)
	hookTimeout = 200 * time.Millisecond
	dir := t.TempDir()
	mark := filepath.Join(dir, "survived")
	t0 := time.Now()
	// The grandchild holds stdout open and would outlive a kill of sh alone.
	h, _, err := runHook(context.Background(), "init", `(sleep 1; touch `+mark+`) & sleep 5`, dir, nil)
	if err == nil || h.Exit != -1 || time.Since(t0) > 3*time.Second {
		t.Fatalf("hook = %+v, %v after %s", h, err, time.Since(t0))
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("the hook's grandchild outlived the timeout")
	}
}
