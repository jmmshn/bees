package scenario_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/cli"
	"github.com/jmmshn/bees/internal/watch"
)

// bs inbox --wait, the agy ring's watcher (bees-13.13.10), run in the background as an agy
// worker runs it: one per worker, invisible to the gate, it exits with a doorbell once new
// mail comes and marks nothing read; mail already unread when it began doesn't fire it;
// it ends at --timeout with ExitNoMail, and when its worker ends.
func TestTheMailWatcher(t *testing.T) {
	x := newToy(t)
	x.spawn("toy-1", "verdict")
	lock := watch.Path(x.ports.CacheDir, "toy-1")
	n := 0
	step := func(args ...string) result { n++; return x.worker("toy-1", n, args...) }
	bg := func(args ...string) string { // a background bs call: its output file
		n++
		x.harness.Step(t, "toy-1", append([]string{"bs&"}, args...)...)
		return x.harness.Await(t, "toy-1", n).Stdout
	}
	armed := func(want bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if _, ok, err := watch.Armed(lock); err == nil && ok == want {
				return
			}
		}
		t.Fatalf("watcher armed never became %v", want)
	}
	output := func(f string) string {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if b, _ := os.ReadFile(f); len(b) > 0 {
				armed(false)
				b, _ = os.ReadFile(f)
				return string(b)
			}
		}
		t.Fatalf("the watcher never said anything in %s", f)
		return ""
	}
	must(t, step("inbox"), 0) // the brief, read
	w := bg("inbox", "--wait")
	armed(true)
	must(t, step("inbox", "--wait"), 2, "already armed")
	must(t, step("done", "--answer", "yes", "--report", "ok"), 0, "handed off") // the gate doesn't count the watcher
	must(t, x.queen("send", "toy-1", "hi"), 0)
	if out := output(w); !strings.Contains(out, "1 unread (queen-toy)") || strings.Contains(out, "hi") {
		t.Fatalf("watcher said %q; want the doorbell, never the body", out)
	}
	w = bg("inbox", "--wait", "--timeout", "300ms") // "hi" is still unread: not new
	if out := output(w); !strings.Contains(out, "no new mail") {
		t.Fatalf("a timed-out watcher said %q", out)
	}
	must(t, step("inbox"), 0, "hi") // nothing was marked read
	w = bg("inbox", "--wait")
	armed(true)
	must(t, x.queen("end", "toy-1"), 0)
	if out := output(w); !strings.Contains(out, "toy-1 has ended") {
		t.Fatalf("the watcher of an ended worker said %q", out)
	}
	if _, err := os.Stat(filepath.Dir(lock)); err != nil {
		t.Fatal(err)
	}
}

// agy's turn hooks, bs hook --harness agy (bees-13.13.10), run as agy runs them: a child
// of the worker's harness, in its workspace, with agy's payload on stdin. A turn's first
// model call with no watcher armed is told to arm one, and with one armed nothing; a turn
// that ends on its own is held with the doorbell while mail is unread, else told to arm a
// watcher while none is; one the keeper cancelled isn't held; the keeper's own agy gets
// nothing.
func TestAgyTurnHooks(t *testing.T) {
	x := newToy(t)
	sp := x.spawn("toy-1", "verdict")
	st, _, _ := x.open().LatestStart(context.Background(), sp.ID)
	hook := func(self int, event, payload string) result {
		t.Helper()
		var out, errb bytes.Buffer
		code := cli.Run(context.Background(), cli.Env{Vars: x.env, Dir: sp.Workspace.Dir, Self: self, Stdin: strings.NewReader(payload)},
			x.ports, []string{"hook", "--harness", "agy", event}, &out, &errb)
		return result{code, out.String(), errb.String()}
	}
	first, later := `{"invocationNum":0,"initialNumSteps":1}`, `{"invocationNum":1}`
	stop, cancel := `{"terminationReason":"NO_TOOL_CALL","fullyIdle":true}`, `{"terminationReason":"USER_CANCELED"}`
	must(t, hook(st.PID, "PreInvocation", first), 0, `{"injectSteps":[{"userMessage":"[bs] Run this command in the background`, "bs inbox --wait")
	if r := hook(st.PID, "PreInvocation", later); r.code != 0 || r.out != "" {
		t.Fatalf("a later model call: %s", r)
	}
	release, _, err := watch.Arm(watch.Path(x.ports.CacheDir, "toy-1"), 4242)
	if err != nil {
		t.Fatal(err)
	}
	if r := hook(st.PID, "PreInvocation", first); r.code != 0 || r.out != "" {
		t.Fatalf("armed: %s", r)
	}
	must(t, x.worker("toy-1", 1, "inbox"), 0) // the brief, read
	if r := hook(st.PID, "Stop", stop); r.code != 0 || r.out != "" {
		t.Fatalf("no unread mail, a watcher armed: %s", r)
	}
	release() // the watcher exited mid-turn, its mail read in the same turn: never idle deaf
	must(t, hook(st.PID, "Stop", stop), 0, `{"decision":"continue","reason":"[bs] Run this command in the background`, "bs inbox --wait")
	must(t, x.queen("send", "toy-1", "hi"), 0)
	must(t, hook(st.PID, "Stop", stop), 0, `{"decision":"continue","reason":"[bs] 1 unread (queen-toy)`)
	if r := hook(st.PID, "Stop", cancel); r.code != 0 || r.out != "" {
		t.Fatalf("a cancelled turn: %s", r)
	}
	if r := hook(os.Getpid(), "PreInvocation", first); r.code != 0 || r.out != "" {
		t.Fatalf("the keeper's agy: %s", r)
	}
}
