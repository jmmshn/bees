package scenario_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/fake"
	reviewcmd "github.com/jmmshn/bees/internal/review/command"
)

// A pr goal in a command workspace whose VCS is the keeper's argv (bees-14.4): a toy vcs
// keeps a workspace's commits in files under <dir>/.vcs (head, parents: "<commit>
// <parent>…" per line, uploaded), and the review host is the command host over a scripted
// cl answering from fake.CLHost (ADR-0010 Amendment 6), so neither git nor gh reads the
// work. The goal checks see only port.VCS and port.Review.

// vcsToy is a clToy with the toy vcs as its [workspace.command] VCS and h as its review
// host.
type vcsToy struct {
	*clToy
	h *fake.CLHost
}

func newVCSToy(t *testing.T) *vcsToy {
	t.Helper()
	root := t.TempDir()
	vcs := filepath.Join(root, "vcs")
	if err := os.WriteFile(vcs, []byte(`#!/bin/sh
# vcs <fact> [args], in a workspace: one JSON object, as [workspace.command] documents.
cd .vcs || exit 3
case $1 in
branch) h=$(cat head) || exit 4; printf '{"tip":"%s"}\n' "$h" ;;
pushed) if test -e uploaded; then printf '{"tip":"%s"}\n' "$(cat uploaded)"; else echo '{}'; fi ;;
has) if test "$2" = t0 || grep -q "^$2 " parents; then echo '{"has":true}'; else echo '{"has":false}'; fi ;;
descends)
	todo=$3
	while test -n "$todo"; do
		c=${todo%% *}; todo=${todo#"$c"}
		if test "$c" = "$2"; then echo '{"descends":true}'; exit 0; fi
		todo=$(echo $todo $(grep "^$c " parents | cut -d' ' -f2-))
	done
	echo '{"descends":false}' ;;
*) exit 2 ;;
esac
`), 0o700); err != nil {
		t.Fatal(err)
	}
	x := &vcsToy{clToy: newCLToy(t, fmt.Sprintf(`create = ["sh", "-c", "mkdir -p \"$1/.vcs\" && echo t0 > \"$1/.vcs/head\" && : > \"$1/.vcs/parents\"", "sh", "{dir}"]
remove = ["rm", "-rf", "{dir}"]
clean = ["true"]
branch = [%[1]q, "branch"]
pushed = [%[1]q, "pushed"]
has = [%[1]q, "has", "{commit}"]
descends = [%[1]q, "descends", "{ancestor}", "{commit}"]`, vcs), ""), h: &fake.CLHost{RepoName: "toy"}}
	host, err := reviewcmd.New(fake.CLToolConfig(), fake.CLTool{H: x.h}.Run, nil)
	if err != nil {
		t.Fatal(err)
	}
	x.ports.Review = host
	return x
}

// commit makes a commit in dir on the host's graph and records it in dir's .vcs.
func (x *vcsToy) commit(dir string) string {
	id := x.h.Commit(dir)
	parent := strings.TrimSpace(x.read(filepath.Join(dir, ".vcs", "head")))
	f, err := os.OpenFile(filepath.Join(dir, ".vcs", "parents"), os.O_APPEND|os.O_WRONLY, 0)
	if err == nil {
		_, err = fmt.Fprintf(f, "%s %s\n", id, parent)
		err = errors.Join(err, f.Close())
	}
	if err != nil {
		x.t.Fatal(err)
	}
	x.write(filepath.Join(dir, ".vcs", "head"), id+"\n")
	return id
}

// upload points change n at dir's head, as the workspace's VCS records it.
func (x *vcsToy) upload(dir string, n int) {
	x.h.Upload(dir, n, false)
	x.write(filepath.Join(dir, ".vcs", "uploaded"), x.h.Head(dir)+"\n")
}

func (x *vcsToy) read(path string) string {
	x.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		x.t.Fatal(err)
	}
	return string(b)
}

// Spawn records the worker's name as the branch its VCS is asked about, and where it
// started; the handoff reads the change's head against it through the commands; a VCS
// command that can't answer is exit 6, never a fail (Rule G).
func TestAPRGoalReadsACommandWorkspacesVCS(t *testing.T) {
	x := newVCSToy(t)
	sp := x.spawn("toy-1", "pr")
	dir := sp.Workspace.Dir
	if ws := sp.Workspace; ws.Branch != "toy-1" || ws.Start != "t0" || ws.Command.VCS == nil {
		t.Fatalf("workspace = %+v", ws)
	}
	must(t, x.worker("toy-1", 1, "done"), 7, "no change from toy-1 since this spawn")
	x.upload(dir, 1)
	must(t, x.worker("toy-1", 2, "done"), 7, "pr_head", "toy-1 is where it started at spawn (t0): commit your work")
	x.commit(dir)
	x.upload(dir, 1)
	must(t, x.worker("toy-1", 3, "done"), 0)
	x.commit(dir)
	must(t, x.worker("toy-1", 4, "done"), 7, "pr_head")
	x.upload(dir, 1)
	must(t, x.worker("toy-1", 5, "done"), 0)
	if err := os.Remove(filepath.Join(dir, ".vcs", "head")); err != nil {
		t.Fatal(err)
	}
	must(t, x.worker("toy-1", 6, "done"), 6, "[workspace.command] branch")
	x.write(filepath.Join(dir, ".vcs", "head"), x.h.Head(dir)+"\n")
	must(t, x.queen("end", "toy-1"), 9, "not complete")
	x.h.Land(1)
	must(t, x.queen("end", "toy-1"), 0)
	must(t, x.queen("prune"), 0, "removed "+dir)
	gone(t, dir)
	if _, ok, err := x.open().LatestSpawn(context.Background(), "toy-1"); err != nil || !ok {
		t.Fatalf("spawn gone from the logbook: %v", err)
	}
}
