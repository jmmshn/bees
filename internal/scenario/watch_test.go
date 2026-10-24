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

// bs inbox --wait, the mail watcher (bees-13.13.10) omp's bs extension runs (ADR-0011),
// run in the background: one per worker, invisible to the gate, it exits with a doorbell once new
// mail comes and marks nothing read; mail already unread when it began doesn't fire it;
// it ends at --timeout with ExitNoMail, and when its worker ends or its harness exits.
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
	must(t, x.keeper("send", "toy-1", "from the keeper"), 0)
	if out := output(w); !strings.Contains(out, `1 unread (keeper) — run: bs inbox   (reply with: bs send queen-toy "...")`) {
		t.Fatalf("watcher said %q; want keeper mail answered to queen-toy (bees-13.92)", out)
	}
	w = bg("inbox", "--wait")
	armed(true)
	must(t, x.queen("end", "toy-1"), 0)
	// bs end stops the harness and records the end: the watcher sees either first
	if out := output(w); !strings.Contains(out, "no mail to watch for") {
		t.Fatalf("the watcher of an ended worker said %q", out)
	}
	if _, err := os.Stat(filepath.Dir(lock)); err != nil {
		t.Fatal(err)
	}
}

// agy's Stop hook, bs hook --harness agy Stop, run as agy runs it: a child of the
// worker's harness, in its workspace, with agy's payload on stdin. A turn that ends on its
// own is held with the doorbell while mail is unread (mail its ring didn't type into a
// working session, bees-13.97), else let stop; one the keeper cancelled isn't held; the
// keeper's own agy gets nothing. PreInvocation's arming is retired: refused as usage.
func TestAgyStopHook(t *testing.T) {
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
	stop, cancel := `{"terminationReason":"NO_TOOL_CALL","fullyIdle":true}`, `{"terminationReason":"USER_CANCELED"}`
	must(t, x.worker("toy-1", 1, "inbox"), 0) // the brief, read
	if r := hook(st.PID, "Stop", stop); r.code != 0 || r.out != "" {
		t.Fatalf("no unread mail: %s", r)
	}
	must(t, x.queen("send", "toy-1", "hi"), 0)
	must(t, hook(st.PID, "Stop", stop), 0, `{"decision":"continue","reason":"[bs] 1 unread (queen-toy)`)
	must(t, x.keeper("send", "toy-1", "from the keeper"), 0)
	must(t, hook(st.PID, "Stop", stop), 0, `2 unread (queen-toy, keeper) — run: bs inbox   (reply with: bs send queen-toy \"...\")`) // bees-13.92
	if r := hook(st.PID, "Stop", cancel); r.code != 0 || r.out != "" {
		t.Fatalf("a cancelled turn: %s", r)
	}
	if r := hook(os.Getpid(), "Stop", stop); r.code != 0 || r.out != "" {
		t.Fatalf("the keeper's agy: %s", r)
	}
	must(t, hook(st.PID, "PreInvocation", `{"invocationNum":0}`), 2, "hook --harness agy Stop")
}
