package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

// ignored reads git's own output: each ignored file, never a collapsed directory, and a
// name git would quote (a space, a non-ASCII byte) as it is; ignoredBy reads a pattern
// as .gitignore does.
func TestIgnoredNamesWhatPruneIgnoreDoesntCover(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin/\nbuild/\n*.o\n*.out\nscratch/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")
	for _, f := range []string{"bin/bs", "bin/sub/a", "cmd/x/build/b", "sub/x.o", "scratch/r.md", "sp ace.out", "é.out"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := &Core{Run: port.Exec{}}
	for _, tc := range []struct {
		let  []string
		want string
	}{
		{nil, "bin/bs, bin/sub/a, cmd/x/build/b, scratch/r.md, sp ace.out, sub/x.o, é.out"},
		{[]string{"bin/"}, "cmd/x/build/b, scratch/r.md, sp ace.out, sub/x.o, é.out"},
		{[]string{"bin/", "build/", "*.o", "*.out"}, "scratch/r.md"},
		{[]string{"cmd/*/build/", "sub/*.o"}, "bin/bs, bin/sub/a, scratch/r.md, sp ace.out, é.out"},
		{[]string{"x/build/", "x.o/", "bs/", "r.md/"}, "bin/bs, bin/sub/a, cmd/x/build/b, scratch/r.md, sp ace.out, sub/x.o, é.out"},
		{[]string{"sp ace.out", "é.out", "bin", "scratch/*"}, "cmd/x/build/b, sub/x.o"},
	} {
		got, err := c.ignored(context.Background(), dir, tc.let)
		if err != nil || got != tc.want {
			t.Errorf("ignored(%q) = %q, %v\n want %q", tc.let, got, err, tc.want)
		}
	}
	if _, err := c.ignored(context.Background(), t.TempDir(), nil); err == nil {
		t.Error("ignored outside a git checkout: no error, want git's")
	}
}
