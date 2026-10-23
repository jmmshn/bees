package core

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jmmshn/bees/internal/port"
)

// writeFiles writes a Launch's Dir as one private <name>-dir-* tree, fills {{dir}}, and a
// second start replaces it; removeFiles takes it whole and never a longer name's.
func TestStartDirIsOnePrivateTreePerStart(t *testing.T) {
	c := &Core{CacheDir: filepath.Join(t.TempDir(), "cache")}
	l := port.Launch{Args: []string{"--add-dir", "{{dir}}", "--settings", "{{s.json}}"}, Files: map[string]string{"s.json": "{}"},
		Dir: map[string]string{".agents/agents/bs-worker.md": "prompt", ".agents/hooks.json": "{}"}}
	other, err := c.writeFiles("bees-12-3", l)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.writeFiles("bees-12", l); err != nil {
		t.Fatal(err)
	}
	args, err := c.writeFiles("bees-12", l)
	if err != nil {
		t.Fatal(err)
	}
	dirs, _ := filepath.Glob(filepath.Join(c.CacheDir, "bees-12-dir-*"))
	if len(dirs) != 1 || args[1] != dirs[0] {
		t.Fatalf("start dirs %q, args %q", dirs, args)
	}
	for rel, want := range l.Dir {
		path := filepath.Join(args[1], rel)
		b, err := os.ReadFile(path)
		if err != nil || string(b) != want {
			t.Fatalf("%s = %q, %v", rel, b, err)
		}
		for p := path; p != c.CacheDir; p = filepath.Dir(p) {
			fi, err := os.Stat(p)
			if err != nil || fi.Mode().Perm()&0o077 != 0 {
				t.Fatalf("%s isn't private: %v, %v", p, fi.Mode(), err)
			}
		}
	}
	if err := c.removeFiles("bees-12"); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(c.CacheDir, "*"))
	if want := []string{other[1], other[3]}; !slices.Equal(left, slices.Sorted(slices.Values(want))) {
		t.Fatalf("left %q, want only bees-12-3's %q", left, want)
	}
}

// A Dir path outside its directory is refused before anything is written or removed.
func TestStartDirRefusesAPathOutsideIt(t *testing.T) {
	c := &Core{CacheDir: t.TempDir()}
	keep := filepath.Join(c.CacheDir, "bees-1-prompt-1.md")
	if err := os.WriteFile(keep, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../x", "/etc/x", ""} {
		l := port.Launch{Dir: map[string]string{rel: "x"}}
		if _, err := c.worstArgs("bees-1", l); err == nil {
			t.Errorf("worstArgs took %q", rel)
		}
		if _, err := c.writeFiles("bees-1", l); err == nil {
			t.Errorf("writeFiles took %q", rel)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("a refused write removed the earlier start's files: %v", err)
	}
}
