package bee_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmmshn/bees/internal/bee"
)

// fakeHerdrBinary puts a herdr on PATH that prints herdr's JSON error envelope with
// code and exits 1, as herdr does.
func fakeHerdrBinary(t *testing.T, code string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho '{\"error\":{\"code\":\"" + code + "\",\"message\":\"from the fake\"}}' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestHerdrAgentNotReadyMeansWaiting(t *testing.T) {
	fakeHerdrBinary(t, "agent_not_ready")
	err := bee.HerdrCLI{}.StartClaude(context.Background(), "w1:p1", "bees-8")
	if !errors.Is(err, bee.ErrWaiting) {
		t.Fatalf("err = %v, want ErrWaiting", err)
	}
}

func TestHerdrOtherErrorsAreNotWaiting(t *testing.T) {
	fakeHerdrBinary(t, "pane_not_found")
	err := bee.HerdrCLI{}.StartClaude(context.Background(), "w1:p1", "bees-8")
	if err == nil || errors.Is(err, bee.ErrWaiting) {
		t.Fatalf("err = %v, want a plain error", err)
	}
}
