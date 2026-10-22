package checks_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/checks"
	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

func TestFillTakesOnlyWholeArgumentPlaceholders(t *testing.T) {
	vals := map[string]checks.Value{"dir": {S: "/w/x"}, "branch": {S: "bees-1", Ref: true}}
	got, err := checks.Fill([]string{"jj", "workspace", "add", "{dir}", "--name", "{branch}"}, vals)
	if err != nil || strings.Join(got, " ") != "jj workspace add /w/x --name bees-1" {
		t.Fatalf("Fill = %q, %v", got, err)
	}
	for _, argv := range [][]string{
		{"git", "--path={dir}"}, // not a whole argument
		{"git", "{nope}"},       // unknown
		{"{dir}", "x"},          // argv[0]
		{},                      // empty
		{"git", "x}"},           // a stray brace
	} {
		if _, err := checks.Fill(argv, vals); !errors.Is(err, checks.ErrBadArgv) {
			t.Errorf("Fill(%q) = %v, want ErrBadArgv", argv, err)
		}
	}
	for _, v := range []checks.Value{{S: "-rf"}, {S: "--upload-pack=x"}, {S: ""}, {S: "a..b", Ref: true}, {S: "x y", Ref: true}} {
		if _, err := checks.Fill([]string{"git", "{v}"}, map[string]checks.Value{"v": v}); !errors.Is(err, checks.ErrBadArgv) {
			t.Errorf("value %+v accepted: %v", v, err)
		}
	}
}

// The command runs in the recorded dir with only PATH, HOME and LANG, and a non-zero
// exit is a result, not an error.
func TestRunScrubsTheEnvAndUsesTheDir(t *testing.T) {
	t.Setenv("BS_PROJECT", "leak")
	t.Setenv("GH_TOKEN", "secret")
	dir := t.TempDir()
	r, err := checks.Run(context.Background(), checks.Cmd{Argv: []string{"sh", "-c", "pwd; env | sort; exit 3"}}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := string(r.Output)
	real, _ := filepath.EvalSymlinks(dir)
	if r.Code != 3 || !strings.Contains(out, real) || strings.Contains(out, "BS_PROJECT") || strings.Contains(out, "secret") {
		t.Fatalf("code %d, output:\n%s", r.Code, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		k, _, _ := strings.Cut(line, "=")
		switch k {
		case "PATH", "HOME", "LANG", "PWD", "SHLVL", "_", "OLDPWD": // the last four are sh's own
		default:
			t.Errorf("env holds %s", line)
		}
	}
}

// A timeout kills the whole process group, a grandchild holding the pipe too (motif 15),
// and is "couldn't run", never a fail.
func TestRunKillsTheProcessGroupAtItsTimeout(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	_, err := checks.Run(context.Background(), checks.Cmd{
		Argv:    []string{"sh", "-c", "sleep 30 & echo $! > " + pidfile + "; wait"},
		Timeout: 300 * time.Millisecond,
	}, t.TempDir(), nil)
	if !errors.Is(err, checks.ErrTimeout) || !errors.Is(err, checks.ErrCantRun) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %s", d)
	}
	b, _ := os.ReadFile(pidfile)
	var pid int
	if _, err := fmt.Sscan(strings.TrimSpace(string(b)), &pid); err != nil {
		t.Fatalf("pid file: %q", b)
	}
	for deadline := time.Now().Add(2 * time.Second); alive(pid); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d outlived the timeout", pid)
		}
	}
}

func TestRunOfAMissingProgramCantRun(t *testing.T) {
	if _, err := checks.Run(context.Background(), checks.Cmd{Argv: []string{"bs-no-such-program"}}, t.TempDir(), nil); !errors.Is(err, checks.ErrCantRun) {
		t.Fatalf("err = %v", err)
	}
	if _, err := checks.Run(context.Background(), checks.Cmd{Argv: []string{"true"}}, "", nil); !errors.Is(err, checks.ErrCantRun) {
		t.Fatalf("no dir: %v", err)
	}
}

// alive is pid running, a zombie counting as gone.
func alive(pid int) bool {
	_, err := proc.Start(pid)
	return err == nil
}

// A ref git can't check is "couldn't run", never "bad ref" (Rule G).
func TestFillOfARefWithNoGitCantRun(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := checks.Fill([]string{"git", "{branch}"}, map[string]checks.Value{"branch": {S: "toy-1", Ref: true}})
	if !errors.Is(err, checks.ErrCantRun) || errors.Is(err, checks.ErrBadArgv) {
		t.Fatalf("Fill = %v; want ErrCantRun", err)
	}
}
