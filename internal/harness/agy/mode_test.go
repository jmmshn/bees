package agy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/harness/agy"
)

// Lines from agy 1.4.6's logs (bees-13.13.14): a bs-started worker asking for auto on a
// host without its sandbox, and one asking for accept-edits.
const (
	start    = `I1023 13:25:53.527790       1 server.go:299] Creating CLI server backend: product=antigravity workspaceDirs=[/repo/wt /cache/bs/%s-dir-2481398278] appDataDir=/home/u/.gemini/antigravity-cli`
	sandbox  = `W1023 13:25:53.530099       1 store.go:31] Terminal sandbox unavailable: AppArmor restricts unprivileged user namespaces (kernel.apparmor_restrict_unprivileged_userns = 1)`
	askAuto  = `I1023 13:25:53.597552       1 overrides.go:82] --mode: overriding agent mode to auto`
	fellBack = `I1023 13:25:53.597572       1 manager.go:1534] auto mode is unavailable because the terminal sandbox is unavailable, falling back to default`
	askEdits = `I1023 14:50:44.163139       1 overrides.go:82] --mode: overriding agent mode to accept-edits`
)

// logs is an Adapter whose agy log dir holds each name → its lines.
func logs(t *testing.T, files map[string][]string) agy.Adapter {
	t.Helper()
	a := settings(t, "")
	dir, err := a.LogDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, lines := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func session(name string) string { return strings.Replace(start, "%s", name, 1) }

// A session's newest log says whether its mode took: the auto start that fell back is
// older than the accept-edits one, and another session's log is never its.
func TestTookReadsTheSessionsNewestLog(t *testing.T) {
	a := logs(t, map[string][]string{
		"cli-20261023_132553.log": {session("w-1"), sandbox, askAuto, fellBack},
		"cli-20261023_145044.log": {session("w-1"), sandbox, askEdits},
		"cli-20261023_150000.log": {session("w-10"), sandbox, askAuto, fellBack},
	})
	got, ok, err := a.Took("w-1")
	if err != nil || !ok {
		t.Fatalf("Took: %v %v", ok, err)
	}
	if got.Asked != agy.Mode || got.Fallback != "" || filepath.Base(got.Log) != "cli-20261023_145044.log" {
		t.Fatalf("got %+v", got)
	}
	// Any session: the newest bs log, w-10's, which fell back.
	got, ok, err = a.Took("")
	if err != nil || !ok || got.Asked != "auto" || got.Fallback != fellBack {
		t.Fatalf("Took(\"\"): %+v %v %v", got, ok, err)
	}
}

// A log no bs launch made, and no log dir at all, is no answer, not an error.
func TestTookNone(t *testing.T) {
	a := logs(t, map[string][]string{"cli-1.log": {strings.Replace(start, "/cache/bs/%s-dir-2481398278", "", 1), askAuto, fellBack}})
	if _, ok, err := a.Took(""); ok || err != nil {
		t.Fatalf("a log of agy run by hand: %v %v", ok, err)
	}
	if _, ok, err := settings(t, "").Took("w-1"); ok || err != nil {
		t.Fatalf("no log dir: %v %v", ok, err)
	}
}

// print mode's own fallback line is read as one too.
func TestTookPrintModeFallback(t *testing.T) {
	line := `W1023 14:50:49.945480       1 printmode.go:836] Print mode: --mode auto is unavailable (terminal sandbox unavailable), continuing in the default mode`
	a := logs(t, map[string][]string{"cli-1.log": {session("w-1"), line}})
	if got, ok, _ := a.Took("w-1"); !ok || got.Fallback != line {
		t.Fatalf("got %+v %v", got, ok)
	}
}
