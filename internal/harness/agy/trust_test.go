package agy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jmmshn/bees/internal/harness/agy"
)

// settings is an agy settings.json in a temp dir, holding s.
func settings(t *testing.T, s string) agy.Adapter {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.json")
	if s != "" {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return agy.Adapter{Settings: p}
}

func read(t *testing.T, a agy.Adapter) map[string]any {
	t.Helper()
	b, err := os.ReadFile(a.Settings)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v:\n%s", err, b)
	}
	return m
}

func trusted(t *testing.T, a agy.Adapter) []string {
	t.Helper()
	var ds []string
	for _, d := range read(t, a)[agy.Trusted].([]any) {
		ds = append(ds, d.(string))
	}
	return ds
}

// Trust adds the exact path to trustedWorkspaces and Untrust removes only it; every other
// key, and every other folder, stays as it was, and the file keeps its mode.
func TestTrustEditsOnlyThatPath(t *testing.T) {
	a := settings(t, `{"permissions": {"allow": ["command(bs)"]}, "trustedWorkspaces": ["/repo"], "zeta": 1}`)
	if err := a.Trust("/repo-WT/bees-1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Trust("/repo-WT/bees-1"); err != nil { // already trusted: unchanged
		t.Fatal(err)
	}
	if got := strings.Join(trusted(t, a), " "); got != "/repo /repo-WT/bees-1" {
		t.Fatalf("trusted after Trust: %s", got)
	}
	if err := a.Untrust("/repo-WT/bees-1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Untrust("/repo-WT/bees-2"); err != nil { // never trusted: unchanged
		t.Fatal(err)
	}
	m := read(t, a)
	if got := strings.Join(trusted(t, a), " "); got != "/repo" || m["zeta"] != 1.0 || m["permissions"].(map[string]any)["allow"].([]any)[0] != "command(bs)" {
		t.Fatalf("settings after Untrust: %v", m)
	}
	if fi, err := os.Stat(a.Settings); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi.Mode(), err)
	}
	if left, _ := filepath.Glob(a.Settings + ".bs-*"); len(left) > 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

// A missing file is made with only the key; Untrust of a missing file makes nothing.
func TestTrustWithoutASettingsFile(t *testing.T) {
	a := settings(t, "")
	if err := a.Untrust("/wt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Settings); !os.IsNotExist(err) {
		t.Fatalf("Untrust made the file: %v", err)
	}
	if err := a.Trust("/wt"); err != nil {
		t.Fatal(err)
	}
	if m := read(t, a); len(m) != 1 || strings.Join(trusted(t, a), " ") != "/wt" {
		t.Fatalf("settings: %v", m)
	}
}

// A file bs can't parse, or a key that isn't a list of paths, is refused and left alone.
func TestTrustRefusesAnUnreadableFile(t *testing.T) {
	for _, s := range []string{`{"trustedWorkspaces": [`, `{"trustedWorkspaces": "/repo"}`} {
		a := settings(t, s)
		err := a.Trust("/wt")
		if err == nil || !strings.Contains(err.Error(), "leaves it as it is") {
			t.Fatalf("%s: %v", s, err)
		}
		if b, _ := os.ReadFile(a.Settings); string(b) != s {
			t.Fatalf("rewrote %s as %s", s, b)
		}
	}
}

// Concurrent bs processes' edits each land: the lock serializes them (motif 15).
func TestTrustIsSerialized(t *testing.T) {
	a := settings(t, `{}`)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if err := a.Trust(filepath.Join("/wt", string(rune('a'+i)))); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := trusted(t, a); len(got) != 20 {
		t.Fatalf("trusted: %v", got)
	}
}
