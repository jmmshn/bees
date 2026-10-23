package command_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jmmshn/bees/internal/checks"
	"github.com/jmmshn/bees/internal/vcs/command"
)

// sh is an argv that runs script, written to a file (an argument holding a brace that
// isn't a whole placeholder is refused), with the placeholders after it as $1, $2, ….
func sh(t *testing.T, script string, args ...string) checks.Cmd {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "vcs-*.sh")
	if err == nil {
		_, err = f.WriteString(script)
	}
	if err == nil {
		err = f.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return checks.Cmd{Argv: append([]string{"sh", f.Name()}, args...)}
}

// Each fact is one JSON object on stdout; what goes to stderr is never read as it.
func TestEachFactIsReadFromItsJSON(t *testing.T) {
	dir := t.TempDir()
	v := command.VCS{Cmds: command.Cmds{
		Branch:   sh(t, `echo noise >&2; echo '{"tip":"c7","extra":1}'`),
		Pushed:   sh(t, `test "$1" = toy-1 && echo '{"tip":"c6"}'`, "{branch}"),
		Has:      sh(t, `test "$1" = c6 && echo '{"has":true}' || echo '{"has":false}'`, "{commit}"),
		Descends: sh(t, `test "$1 $2" = "c6 c7" && echo '{"descends":true}' || echo '{"descends":false}'`, "{ancestor}", "{commit}"),
	}}
	ctx := context.Background()
	if tip, ok, err := v.Branch(ctx, dir, "toy-1"); err != nil || !ok || tip != "c7" {
		t.Errorf("Branch = %q, %v, %v; want c7", tip, ok, err)
	}
	if tip, ok, err := v.Pushed(ctx, dir, "toy-1"); err != nil || !ok || tip != "c6" {
		t.Errorf("Pushed = %q, %v, %v; want c6", tip, ok, err)
	}
	for commit, want := range map[string]bool{"c6": true, "c9": false} {
		if has, err := v.Has(ctx, dir, commit); err != nil || has != want {
			t.Errorf("Has(%s) = %v, %v; want %v", commit, has, err, want)
		}
	}
	if ok, err := v.Descends(ctx, dir, "c6", "c7"); err != nil || !ok {
		t.Errorf("Descends(c6, c7) = %v, %v; want true", ok, err)
	}
	if ok, err := v.Descends(ctx, dir, "c7", "c6"); err != nil || ok {
		t.Errorf("Descends(c7, c6) = %v, %v; want false", ok, err)
	}
}

// A tip absent, "" or null is none: ok false, never an error.
func TestNoTipIsNone(t *testing.T) {
	for _, out := range []string{`{}`, `{"tip":""}`, `{"tip":null}`} {
		v := command.VCS{Cmds: command.Cmds{Branch: sh(t, `echo '`+out+`'`)}}
		if tip, ok, err := v.Branch(context.Background(), t.TempDir(), "toy-1"); err != nil || ok || tip != "" {
			t.Errorf("%s: Branch = %q, %v, %v; want none", out, tip, ok, err)
		}
	}
}

// A command that exits non-zero, prints no JSON object, or omits has/descends couldn't
// say: an error, never "no" (Rule G).
func TestAnUnclearAnswerIsAnError(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]checks.Cmd{
		"exit 1":       sh(t, `echo '{"has":false}'; exit 1`),
		"not json":     sh(t, `echo no`),
		"two objects":  sh(t, `echo '{"has":true}{"has":false}'`),
		"no key":       sh(t, `echo '{}'`),
		"wrong type":   sh(t, `echo '{"has":"yes"}'`),
		"not on PATH":  {Argv: []string{"no-such-vcs-tool"}},
		"array":        sh(t, `echo '[]'`),
		"empty output": sh(t, `true`),
	} {
		v := command.VCS{Cmds: command.Cmds{Has: c}, Getenv: os.Getenv}
		if has, err := v.Has(ctx, t.TempDir(), "c1"); err == nil {
			t.Errorf("%s: Has = %v, nil; want an error", name, has)
		}
	}
	v := command.VCS{Cmds: command.Cmds{Branch: sh(t, `exit 2`)}}
	if _, _, err := v.Branch(ctx, t.TempDir(), "toy-1"); !errors.Is(err, checks.ErrCantRun) {
		t.Errorf("Branch exiting 2: err = %v; want ErrCantRun", err)
	}
}

// A commit the review host names fills one argument, and one read as a flag is refused.
func TestACommitLikeAFlagIsRefused(t *testing.T) {
	v := command.VCS{Cmds: command.Cmds{Has: sh(t, `echo '{"has":true}'`, "{commit}")}}
	if _, err := v.Has(context.Background(), t.TempDir(), "--all"); !errors.Is(err, checks.ErrBadArgv) {
		t.Errorf("Has(--all): err = %v; want ErrBadArgv", err)
	}
}
