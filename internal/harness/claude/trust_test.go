package claude_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/harness/claude"
)

// config is a Claude home in a temp dir whose .claude.json holds s ("" none).
func config(t *testing.T, s string) claude.Adapter {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	a := claude.Adapter{Home: t.TempDir()}
	if s != "" {
		if err := os.WriteFile(filepath.Join(a.Home, ".claude.json"), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func readConfig(t *testing.T, a claude.Adapter) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.Home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v:\n%s", err, b)
	}
	return m
}

func projects(t *testing.T, a claude.Adapter) map[string]any {
	t.Helper()
	ps, _ := readConfig(t, a)["projects"].(map[string]any)
	return ps
}

// bees-13.86: Trust marks a command workspace's folder trusted, keeping the rest of its
// entry and of the config; Untrust drops only that entry, and the file keeps its mode.
func TestTrustMarksAFolderOutsideGit(t *testing.T) {
	cmd := filepath.Join(t.TempDir(), "clients", "toy-1")
	if err := os.MkdirAll(cmd, 0o755); err != nil {
		t.Fatal(err)
	}
	a := config(t, `{"numStartups": 3, "oauthAccount": {"email": "a<b>&c"}, "projects": {"/repo": {"hasTrustDialogAccepted": true}, "`+cmd+`": {"allowedTools": ["Bash"]}}}`)
	if err := a.Trust(cmd); err != nil {
		t.Fatal(err)
	}
	if err := a.Trust(cmd); err != nil { // already trusted: unchanged
		t.Fatal(err)
	}
	p := projects(t, a)[cmd].(map[string]any)
	if p[claude.TrustKey] != true || p["allowedTools"].([]any)[0] != "Bash" {
		t.Fatalf("entry after Trust: %v", p)
	}
	b, _ := os.ReadFile(filepath.Join(a.Home, ".claude.json"))
	if !strings.Contains(string(b), `"a<b>&c"`) {
		t.Fatalf("escaped what Claude wrote:\n%s", b)
	}
	if err := a.Untrust(cmd); err != nil {
		t.Fatal(err)
	}
	m := readConfig(t, a)
	ps := m["projects"].(map[string]any)
	if _, ok := ps[cmd]; ok || ps["/repo"] == nil || m["numStartups"] != 3.0 {
		t.Fatalf("config after Untrust: %v", m)
	}
	if fi, err := os.Stat(filepath.Join(a.Home, ".claude.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi.Mode(), err)
	}
	for _, left := range []string{".claude.json.bs-*", ".claude.json.lock"} {
		if got, _ := filepath.Glob(filepath.Join(a.Home, left)); len(got) > 0 {
			t.Fatalf("left behind: %v", got)
		}
	}
}

// A folder in a git checkout, a worktree's included, is left alone: Claude trusts it
// through its repo, the keeper's to trust. Untrust of a folder with no trusted entry
// writes nothing.
func TestTrustLeavesGitCheckoutsToTheKeeper(t *testing.T) {
	repo := t.TempDir()
	wt := filepath.Join(repo, "sub", "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := config(t, "")
	for _, d := range []string{repo, filepath.Join(repo, "sub"), wt} {
		if err := a.Trust(d); err != nil {
			t.Fatal(err)
		}
		if err := a.Untrust(d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(a.Home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("wrote Claude's config: %v", err)
	}
	b := config(t, `{"projects": {"/gone": {"allowedTools": []}}}`)
	if err := b.Untrust("/gone"); err != nil {
		t.Fatal(err)
	}
	if projects(t, b)["/gone"] == nil {
		t.Fatal("Untrust dropped an entry that didn't say trusted")
	}
}

// A config bs can't parse is refused and left alone.
func TestTrustRefusesAnUnreadableConfig(t *testing.T) {
	for _, s := range []string{`{"projects": {`, `{"projects": []}`, `{"projects": {"DIR": 1}}`} {
		dir := t.TempDir()
		s = strings.ReplaceAll(s, "DIR", dir)
		a := config(t, s)
		err := a.Trust(dir)
		if err == nil || !strings.Contains(err.Error(), "leaves it as it is") {
			t.Fatalf("%s: %v", s, err)
		}
		if b, _ := os.ReadFile(filepath.Join(a.Home, ".claude.json")); string(b) != s {
			t.Fatalf("rewrote %s as %s", s, b)
		}
	}
}

// bs waits on Claude's own lock on its config, takes over one left stale, and its
// concurrent edits each land (motif 15).
func TestTrustTakesClaudesLock(t *testing.T) {
	a := config(t, `{}`)
	lock := filepath.Join(a.Home, ".claude.json.lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() { done <- a.Trust(t.TempDir()) }()
	select {
	case err := <-done:
		t.Fatalf("Trust didn't wait for the lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if err := a.Trust(t.TempDir()); err != nil {
		t.Fatalf("a stale lock: %v", err)
	}
	var wg sync.WaitGroup
	for range 20 {
		d := t.TempDir()
		wg.Go(func() {
			if err := a.Trust(d); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := projects(t, a); len(got) != 22 {
		t.Fatalf("projects: %v", got)
	}
}

// CLAUDE_CONFIG_DIR moves Claude's config, as it does for Claude.
func TestTrustFollowsClaudeConfigDir(t *testing.T) {
	d := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", d)
	if err := (claude.Adapter{Home: t.TempDir()}).Trust(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, ".claude.json")); err != nil {
		t.Fatal(err)
	}
}
