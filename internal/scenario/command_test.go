package scenario_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/config"
)

// A command workspace and a custom goal (bees-13.11, ADR-0010 Workspaces and Goals): the
// keeper's argv make, check and remove a worker's directory, and a CL-shaped goal's cmd:
// checks say whether its change was sent and submitted. A fake cl keeps its state in
// files: <state>/<name>.sent once the worker mails its CL, <name>.submitted once it lands.

// clToy is a toy whose project config makes a command workspace with create and remove
// (plain directories: mkdir, rm) and a custom goal cl, with ws's [workspace.command] keys
// and extra appended to the project file.
type clToy struct {
	*toy
	state, cl, ws string
}

func newCLToy(t *testing.T, command, extra string) *clToy {
	t.Helper()
	x := &clToy{toy: newToy(t)}
	root := filepath.Dir(x.repo)
	x.state, x.ws = filepath.Join(root, "cl-state"), filepath.Join(root, "toy-WS")
	x.cl = filepath.Join(root, "bin", "cl")
	for _, d := range []string{x.state, x.ws, filepath.Dir(x.cl), filepath.Join(x.config, "projects")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// cl sent|submitted <name>: exit 0 only when the state file is there, saying so.
	if err := os.WriteFile(x.cl, []byte(`#!/bin/sh
f="`+x.state+`/$2.$1"
if test -e "$f"; then echo "$2: $1"; exit 0; fi
echo "$2: not $1"; exit 1
`), 0o700); err != nil {
		t.Fatal(err)
	}
	if command == "" {
		command = `create = ["mkdir", "{dir}"]
remove = ["rm", "-rf", "{dir}"]
clean = ["sh", "-c", "test ! -e unsaved"]`
	}
	x.write(filepath.Join(x.config, "projects", project+".toml"), fmt.Sprintf(`[workspace.command]
%s
dir = %q

[checks.cl_sent]
run = [%q, "sent", "{name}"]
[checks.cl_submitted]
run = [%q, "submitted", "{name}"]

[goal.cl]
handoff = ["cmd:cl_sent"]
complete = ["cmd:cl_submitted"]
prune = ["cmd:cl_submitted"]
%s`, command, x.ws+"/{name}", x.cl, x.cl, extra))
	return x
}

func (x *clToy) mark(name, state string) { x.write(filepath.Join(x.state, name+"."+state), "") }

func gone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s is still there (err %v)", path, err)
	}
}

func noSpawn(t *testing.T, x *clToy, name string) {
	t.Helper()
	if _, err := os.Stat(x.logbook); os.IsNotExist(err) {
		return // a refused first spawn makes no logbook (R48)
	}
	if _, ok, err := x.open().LatestSpawn(context.Background(), name); err != nil || ok {
		t.Fatalf("a spawn of %s was recorded (err %v)", name, err)
	}
}

// The live test's flow, with fakes: spawn makes the directory with create; the handoff
// fails until the CL is sent, naming what cl said; bs end is exit 9 until it is
// submitted; prune runs clean, then remove.
func TestACLGoalInACommandWorkspace(t *testing.T) {
	x := newCLToy(t, "", "")
	x.bead("toy-1", "work on toy-1")
	r := x.queen("spawn", "--goal", "cl", "--label", "toy one", "toy-1")
	must(t, r, 0, "a command workspace in a git checkout")
	sp, ok, err := x.open().LatestSpawn(context.Background(), "toy-1")
	if err != nil || !ok {
		t.Fatalf("not recorded: %v\n%s", err, r)
	}
	dir := filepath.Join(x.ws, "toy-1")
	if sp.Workspace.Kind != config.CommandKind || sp.Workspace.Dir != dir || sp.Workspace.Branch != "" || sp.Workspace.Command == nil {
		t.Fatalf("workspace = %+v", sp.Workspace)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("create made no %s: %v", dir, err)
	}
	if g := sp.Goal; g.Type != "cl" || g.AtHandoff || g.Cmds["cl_sent"].Argv[0] != x.cl {
		t.Fatalf("goal = %+v", g)
	}
	must(t, x.worker("toy-1", 1, "done"), 7, "cmd:cl_sent", "exited 1: toy-1: not sent")
	x.mark("toy-1", "sent")
	must(t, x.worker("toy-1", 2, "done"), 0)
	must(t, x.queen("end", "toy-1"), 9, "not complete", "cmd:cl_submitted", x.cl+" submitted toy-1 exited 1: toy-1: not submitted")
	x.mark("toy-1", "submitted")
	must(t, x.queen("end", "toy-1"), 0)
	x.write(filepath.Join(dir, "unsaved"), "a draft")
	must(t, x.queen("prune"), 3, "kept", "clean says "+dir+" holds work that would be lost")
	if err := os.Remove(filepath.Join(dir, "unsaved")); err != nil {
		t.Fatal(err)
	}
	must(t, x.queen("prune"), 0, "removed "+dir)
	gone(t, dir)
}

// A create that fails, or runs past its timeout, runs remove: nothing is left on disk and
// nothing recorded (motif 8), and a later spawn isn't refused by what it left.
func TestACreateThatFailsLeavesNothing(t *testing.T) {
	for name, create := range map[string]string{
		"fails": `create = ["sh", "-c", "mkdir \"$1\"; echo no room; exit 3", "sh", "{dir}"]`,
		"times out": `create = ["sh", "-c", "mkdir \"$1\"; sleep 30", "sh", "{dir}"]
timeout = "300ms"`,
		"makes no dir": `create = ["true"]`,
	} {
		t.Run(name, func(t *testing.T) {
			x := newCLToy(t, create+"\nremove = [\"rm\", \"-rf\", \"{dir}\"]\nclean = [\"true\"]", "")
			x.bead("toy-1", "work on toy-1")
			start := time.Now()
			r := x.queen("spawn", "--goal", "cl", "--label", "toy one", "toy-1")
			must(t, r, 1, "[workspace.command] create", "nothing was recorded")
			if d := time.Since(start); d > 10*time.Second {
				t.Fatalf("took %s", d)
			}
			gone(t, filepath.Join(x.ws, "toy-1"))
			noSpawn(t, x, "toy-1")
		})
	}
}

// A directory that exists where a spawn would make one, with no spawn recorded on it, is
// refused: nobody's entry says whose it is.
func TestAnUnrecordedDirRefusesASpawn(t *testing.T) {
	x := newCLToy(t, "", "")
	dir := filepath.Join(x.ws, "toy-1")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	x.bead("toy-1", "work on toy-1")
	must(t, x.queen("spawn", "--goal", "cl", "--label", "toy one", "toy-1"), 2, dir+" already exists, and no spawn of toy-1 recorded it")
	noSpawn(t, x, "toy-1")
}

// vcsKeys are [workspace.command] VCS keys that load: the refusals never run them.
const vcsKeys = `
branch = ["true"]
pushed = ["true"]
has = ["true"]
descends = ["true"]`

// The spawn-time refusals, each before anything is made: no clean, a check's argv[0] on
// no PATH, a placeholder a command workspace can't fill, a built-in check that reads a
// git branch (with or without a VCS), a goal that hands off a git branch, and a pr goal
// in a workspace with no VCS (bees-14.4).
func TestCommandSpawnRefusals(t *testing.T) {
	plain := `create = ["mkdir", "{dir}"]` + "\n" + `remove = ["rm", "-rf", "{dir}"]` + "\n" + `clean = ["true"]`
	for name, tc := range map[string]struct{ command, extra, goal, want string }{
		"no clean":            {`create = ["mkdir", "{dir}"]` + "\n" + `remove = ["rm", "-rf", "{dir}"]`, "", "cl", "[workspace.command] has no clean"},
		"argv[0] missing":     {"", "[checks.gone]\nrun = [\"bs-no-such-cl\"]\n[goal.gone]\nhandoff = [\"cmd:gone\"]\n", "gone", "checks.gone.run: bs-no-such-cl is not on PATH"},
		"no branch to fill":   {"", "[checks.br]\nrun = [\"true\", \"{branch}\"]\n[goal.br]\nhandoff = [\"cmd:br\"]\n", "br", "checks.br.run can't run in a command workspace"},
		"a branch check":      {"", "[goal.ahead]\nhandoff = [\"branch_ahead\"]\n", "ahead", "branch_ahead read a git branch, which a command workspace has none of"},
		"a git check":         {plain + vcsKeys, "[goal.merged]\nhandoff = [\"pr_open\"]\ncomplete = [\"branch_merged\"]\n", "merged", "branch_merged read a git branch"},
		"a branch goal":       {"", "", "branch", "a command workspace has no git branch to cut at a change's head"},
		"a branch goal, VCS":  {plain + vcsKeys, "", "branch:ready", "a command workspace has no git branch"},
		"a pr check, no VCS":  {"", "[goal.open]\nhandoff = [\"pr_open\"]\n", "open", "a command workspace with no VCS has no branch to hand off: set [workspace.command] branch, pushed, has and descends"},
		"vcs argv[0] missing": {plain + strings.Replace(vcsKeys, `has = ["true"]`, `has = ["bs-no-such-vcs"]`, 1), "", "cl", "workspace.command.has: bs-no-such-vcs is not on PATH"},
		"create missing":      {`create = ["bs-no-such-mk", "{dir}"]` + "\n" + `remove = ["rm", "-rf", "{dir}"]` + "\n" + `clean = ["true"]`, "", "cl", "workspace.command.create: bs-no-such-mk is not on PATH"},
		"a custom state":      {"", "", "cl:ready", "the custom type cl takes no :<state> or =<target>"},
	} {
		t.Run(name, func(t *testing.T) {
			x := newCLToy(t, tc.command, tc.extra)
			x.bead("toy-1", "work on toy-1")
			must(t, x.queen("spawn", "--goal", tc.goal, "--label", "toy one", "toy-1"), 2, tc.want)
			gone(t, filepath.Join(x.ws, "toy-1"))
			noSpawn(t, x, "toy-1")
		})
	}
}

// A check is a yes only by exit 0: a command that couldn't run (its program gone since
// spawn) is exit 6, never a pass or a fail.
func TestACheckThatCantRunIsNeverAnAnswer(t *testing.T) {
	x := newCLToy(t, "", "")
	x.spawn("toy-1", "cl")
	if err := os.Remove(x.cl); err != nil {
		t.Fatal(err)
	}
	must(t, x.worker("toy-1", 1, "done"), 6, "cmd:cl_sent")
}

// The goal frozen at spawn is what runs: a [checks] argv edited later changes nothing for
// a live worker. The fragment is the keeper's file, hashed into the goal, and a file
// changed since refuses a resume until an amend records it again.
func TestACustomGoalIsFrozenAtSpawn(t *testing.T) {
	fragment := filepath.Join(t.TempDir(), "cl.md")
	text := "`--goal cl`: a CL, complete once submitted.\n<!-- worker -->\n- Mail it with `cl mail`.\n"
	if err := os.WriteFile(fragment, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	x := newCLToy(t, "", fmt.Sprintf("fragment = %q\n", fragment))
	sp := x.spawn("toy-1", "cl")
	if want := fmt.Sprintf("%x", sha256.Sum256([]byte(text))); sp.Goal.Fragment != want {
		t.Fatalf("fragment hash = %q, want %q", sp.Goal.Fragment, want)
	}
	x.write(filepath.Join(x.config, "projects", project+".toml"), strings.Replace(x.read(filepath.Join(x.config, "projects", project+".toml")),
		`"sent", "{name}"]`, `"submitted", "{name}"]`, 1))
	x.mark("toy-1", "sent")
	must(t, x.worker("toy-1", 1, "done"), 0) // the frozen cl sent, not config's cl submitted
	e, err := x.open().Goal(context.Background(), sp)
	if err != nil || e.Cmds["cl_sent"].Argv[1] != "sent" {
		t.Fatalf("goal = %+v, %v", e, err)
	}
	if !strings.Contains(x.prompt("toy-1"), "Mail it with `cl mail`") {
		t.Fatalf("the worker's prompt lacks the keeper's fragment")
	}
}

// A discard lets prune remove a command workspace whose goal never completed; until then
// the frozen prune rule keeps it.
func TestADiscardedCommandWorkspaceIsRemoved(t *testing.T) {
	x := newCLToy(t, "", "")
	x.spawn("toy-1", "cl")
	dir := filepath.Join(x.ws, "toy-1")
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	must(t, x.queen("prune"), 3, "kept", dir)
	must(t, x.keeper("goal", "toy-1", "discard"), 0)
	must(t, x.queen("prune"), 0, "removed "+dir)
	gone(t, dir)
}

// A re-spawn of an ended worker whose directory was kept reuses it, with the commands
// its first spawn froze.
func TestARespawnReusesItsKeptDir(t *testing.T) {
	x := newCLToy(t, "", "")
	sp := x.spawn("toy-1", "cl")
	x.write(filepath.Join(sp.Workspace.Dir, "work.txt"), "kept")
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	must(t, x.queen("spawn", "--goal", "cl", "toy-1"), 0, "in its kept directory "+sp.Workspace.Dir)
	if b, err := os.ReadFile(filepath.Join(sp.Workspace.Dir, "work.txt")); err != nil || string(b) != "kept" {
		t.Fatalf("work.txt = %q, %v", b, err)
	}
}

// read is path's contents.
func (x *toy) read(path string) string {
	x.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		x.t.Fatal(err)
	}
	return string(b)
}

// prompt is the prompt file worker name's last start was given.
func (x *toy) prompt(name string) string {
	x.t.Helper()
	m, err := filepath.Glob(filepath.Join(x.ports.CacheDir, name+"-prompt-*.md"))
	if err != nil || len(m) != 1 {
		x.t.Fatalf("prompt files of %s: %v, %v", name, m, err)
	}
	return x.read(m[0])
}

// A resume reads the keeper's fragment again, and refuses one changed since the goal was
// recorded: the worker would work to wording its record doesn't hold. bs goal records
// the new one, and the resume goes ahead.
func TestAResumeRefusesAChangedFragment(t *testing.T) {
	fragment := filepath.Join(t.TempDir(), "cl.md")
	x := newCLToy(t, "", fmt.Sprintf("fragment = %q\n", fragment))
	x.write(fragment, "`--goal cl`: a CL.\n")
	x.spawn("toy-1", "cl")
	x.kill("toy-1")
	x.write(fragment, "`--goal cl`: a CL, reworded.\n")
	r := x.queen("spawn", "--goal", "cl", "toy-1")
	must(t, r, 2, "[goal.cl] fragment "+fragment+" changed since its goal was recorded", "next: bs goal toy-1 cl")
	must(t, x.next(r), 0)
	must(t, x.queen("spawn", "--goal", "cl", "toy-1"), 0, "resumed toy-1")
	if !strings.Contains(x.prompt("toy-1"), "a CL, reworded.") {
		t.Fatal("the resume's prompt lacks the fragment its amend recorded")
	}
}

// Prune runs remove only on the directory create made: one remade there by hand since is
// kept, named, never removed. The old one is moved aside, not deleted: a filesystem may
// hand a deleted directory's inode straight to the next one (CI's did), which the inode
// check can't tell apart.
func TestPruneKeepsADirRemadeByHand(t *testing.T) {
	x := newCLToy(t, "", "")
	sp := x.spawn("toy-1", "cl")
	if sp.Workspace.Command.Ino == 0 {
		t.Fatal("no inode recorded")
	}
	must(t, x.queen("end", "--incomplete", "toy-1"), 0)
	must(t, x.keeper("goal", "toy-1", "discard"), 0)
	if err := os.Rename(sp.Workspace.Dir, sp.Workspace.Dir+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sp.Workspace.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	must(t, x.queen("prune"), 3, "kept", "no longer the directory its spawn made")
	if _, err := os.Stat(sp.Workspace.Dir); err != nil {
		t.Fatalf("prune removed it: %v", err)
	}
}

// review.host = "command" (ADR-0010 Amendment 6): the keeper's [review.command] argv, a
// shell script here, answer for the review host in JSON, read from the config files as a
// real bs reads them. A review goal's target is viewed at spawn (null: no such change, a
// refusal) and its report posted through post_review.
func TestCommandReviewHostFromConfig(t *testing.T) {
	x := newToy(t)
	root := filepath.Dir(x.repo)
	cl, posted := filepath.Join(root, "bin", "cl"), filepath.Join(root, "posted")
	if err := os.MkdirAll(filepath.Dir(cl), 0o700); err != nil {
		t.Fatal(err)
	}
	// cl repo | view <url> | ci <url> | changes <dir> | review <url> <file> <marker>
	x.write(cl, `#!/bin/sh
case "$1" in
repo) echo '{"repo": "toy", "protected": false}' ;;
changes) echo '[]' ;;
view) case "$2" in
	https://cl.test/toy/6) echo '{"url": "'"$2"'", "state": "open", "head": "", "head_commit": "abc", "base": "trunk", "created": "2026-01-01T00:00:00Z"}' ;;
	*) echo null ;;
	esac ;;
ci) echo '{"checks": [], "required": []}' ;;
review)
	if test -e "`+posted+`" && grep -q "$4" "`+posted+`"; then echo '{"url": "'"$2"'#r1"}'; exit 0; fi
	cat "$3" >> "`+posted+`"; echo '{"url": "'"$2"'#r1"}' ;;
*) echo "cl: unknown $1" >&2; exit 2 ;;
esac
`)
	if err := os.Chmod(cl, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(x.config, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	frag := filepath.Join(root, "cl.md")
	x.write(frag, "<!-- worker -->\nUpload with cl upload.\n<!-- queen -->\nLand with cl submit.\n")
	x.write(filepath.Join(x.config, "projects", project+".toml"), fmt.Sprintf(`[review]
host = "command"
fragment = %q

[review.command]
url = 'https://cl\.test/(?P<repo>[^/]+)/(?P<number>[0-9]+)'
repo = [%[2]q, "repo"]
changes = [%[2]q, "changes", "{dir}"]
view = [%[2]q, "view", "{url}"]
ci = [%[2]q, "ci", "{url}"]
post_review = [%[2]q, "review", "{url}", "{body_file}", "{marker}"]
merge_deny = ["cl submit*"]
`, frag, cl))
	x.bead("toy-1", "x")
	must(t, x.queen("spawn", "--label", "x", "--goal", "review=https://cl.test/toy/9", "toy-1"), 2, "no such change")
	must(t, x.queen("spawn", "--label", "x", "--goal", "review=https://github.com/o/r/pull/1", "toy-1"), 2, "not a change URL")
	sp := x.spawn("toy-1", "review=https://cl.test/toy/6")
	must(t, x.keeper("done", "--as", "toy-1", "--answer", "changes", "--report", "rename it"), 0, "review https://cl.test/toy/6#r1")
	b, err := os.ReadFile(posted)
	if err != nil || strings.Count(string(b), "rename it") != 1 || !strings.Contains(string(b), "bs:"+string(sp.ID)) {
		t.Fatalf("posted reviews: %q, %v", b, err)
	}
}
