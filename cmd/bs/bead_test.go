package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBd puts a bd on PATH that appends its arguments, one call per line, to the
// returned log and prints out.
func fakeBd(t *testing.T, out string) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	if err := os.WriteFile(filepath.Join(dir, "out"), []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\ncat " + filepath.Join(dir, "out") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BS_PROJECT", "web_2")
	return log
}

func calls(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// bd's id wildcard is a SQL LIKE, so "web_2-*" also matches another project, webx2.
const queryOut = `[
 {"id":"web_2-1","title":"the epic","status":"open","priority":1,"issue_type":"epic"},
 {"id":"web_2-1.2","title":"a task","status":"in_progress","priority":2,"issue_type":"task"},
 {"id":"webx2-1","title":"another project","status":"open","priority":2,"issue_type":"epic"}
]`

func TestBeadLsListsOnlyTheProjectsOpenBeads(t *testing.T) {
	log := fakeBd(t, queryOut)
	out, stderr, code := bs(t, "bead", "ls")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(out, "web_2-1 ") || !strings.Contains(out, "web_2-1.2 ") || !strings.Contains(out, "a task") {
		t.Errorf("out = %q, want both web_2 beads", out)
	}
	if strings.Contains(out, "webx2") {
		t.Errorf("out = %q lists another project's bead", out)
	}
	if got := calls(t, log); !strings.Contains(got, `query --json --limit 0 id="web_2-*" AND status!=closed`) {
		t.Errorf("bd calls = %q", got)
	}
}

func TestBeadLsAllAndExtraFilters(t *testing.T) {
	log := fakeBd(t, queryOut)
	if _, stderr, code := bs(t, "bead", "ls", "--all", "priority<=1", "OR", "type=bug"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := calls(t, log); !strings.Contains(got, `id="web_2-*" AND (priority<=1 OR type=bug)`) || strings.Contains(got, "closed") {
		t.Errorf("bd calls = %q, want closed beads kept and the filter ANDed in parentheses", got)
	}
}

func TestBeadNewFilesUnderAProjectEpic(t *testing.T) {
	log := fakeBd(t, "web_2-1.3\n")
	out, stderr, code := bs(t, "bead", "new", "--parent", "web_2-1", "--priority", "1", "--type", "bug", "fix the parser")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if out != "web_2-1.3\n" {
		t.Errorf("out = %q, want the new id", out)
	}
	// --force: bd refuses ids that don't carry the store's own prefix.
	if got := calls(t, log); got != "create --silent --force --parent web_2-1 --title fix the parser --type bug --priority 1\n" {
		t.Errorf("bd calls = %q", got)
	}
}

func TestBeadNewRefusesBeadsOutsideAnEpicOfTheProject(t *testing.T) {
	for _, args := range [][]string{
		{"bead", "new", "a title"},                          // no parent: top-level beads are epics
		{"bead", "new", "--parent", "webx2-1", "a title"},   // another project
		{"bead", "new", "--parent", "beads-x7q", "a title"}, // not a project bead at all
	} {
		log := fakeBd(t, "")
		_, stderr, code := bs(t, args...)
		if code == 0 {
			t.Errorf("%q succeeded", args)
		}
		if _, err := os.Stat(log); err == nil {
			t.Errorf("%q called bd: %s", args, calls(t, log))
		}
		if args[2] != "--parent" && !strings.Contains(stderr, "bs epic") {
			t.Errorf("%q: stderr = %q, want a pointer to bs epic", args, stderr)
		}
	}
}

func TestEpicPointsAtBeadNew(t *testing.T) {
	fakeBd(t, "[]")
	out, stderr, code := bs(t, "epic", "parser rewrite")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.HasPrefix(out, "web_2-1\n") || !strings.Contains(out, `bs bead new --parent web_2-1 "..."`) {
		t.Errorf("out = %q", out)
	}
}
