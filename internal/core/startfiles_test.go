package core

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// With a start dir set, the per-start dir goes there and the files stay in the cache dir
// (bees-13.13.28); removeFiles takes it, and a start dir an older bs left in the cache dir.
func TestStartDirGoesInTheStartDir(t *testing.T) {
	root := t.TempDir()
	c := &Core{CacheDir: filepath.Join(root, "cache"), StartDir: filepath.Join(root, "run", "bees", "bs")}
	legacy := filepath.Join(c.CacheDir, "bees-12-dir-7", ".agents")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	l := port.Launch{Args: []string{"--add-dir", "{{dir}}", "--settings", "{{s.json}}"}, Files: map[string]string{"s.json": "{}"},
		Dir: map[string]string{".agents/hooks.json": "{}"}}
	args, err := c.writeFiles("bees-12", l)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(args[1]) != c.StartDir || filepath.Dir(args[3]) != c.CacheDir {
		t.Fatalf("args %q: want the dir in %s, the file in %s", args, c.StartDir, c.CacheDir)
	}
	if fi, err := os.Stat(c.StartDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("start dir %v, %v", fi, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("the cache dir's old start dir is still there: %v", err)
	}
	worst, err := c.worstArgs("bees-12", l)
	if err != nil || filepath.Dir(worst[1]) != c.StartDir {
		t.Errorf("worst args %q, %v", worst, err)
	}
	if err := c.removeFiles("bees-12"); err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{c.CacheDir, c.StartDir} {
		if left, _ := filepath.Glob(filepath.Join(in, "*")); len(left) != 0 {
			t.Errorf("left %q", left)
		}
	}
}

// A start dir others can write, or one inside such a dir, is refused before a start dir
// is made in it: it holds the hooks agy runs (bees-13.13.28).
func TestStartDirRefusesADirOthersCanWrite(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "tmp")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o1777); err != nil {
		t.Fatal(err)
	}
	l := port.Launch{Args: []string{"--add-dir", "{{dir}}"}, Dir: map[string]string{".agents/hooks.json": "{}"}}
	for _, start := range []string{shared, filepath.Join(shared, "bs")} {
		c := &Core{CacheDir: filepath.Join(root, "cache"), StartDir: start}
		if _, err := c.writeFiles("bees-12", l); err == nil || !strings.Contains(err.Error(), shared) {
			t.Errorf("start dir %s: %v", start, err)
		}
		if dirs, _ := filepath.Glob(filepath.Join(start, "bees-12-dir-*")); len(dirs) != 0 {
			t.Errorf("made %q", dirs)
		}
	}
}
