package fake_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/cmd/bsx/internal/fake"
	"github.com/jmmshn/bees/cmd/bsx/internal/herdr"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

type rig struct {
	h     *fake.Herdr
	harn  fake.Harness
	calls chan fake.Call
}

// newRig is a fake herdr whose claude is beesfake, relaying bs calls to calls.
func newRig(t *testing.T) rig {
	t.Helper()
	state, err := os.MkdirTemp("", "bf") // short: socket paths must fit sun_path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	relay := filepath.Join(state, "relay.sock")
	r := rig{harn: fake.Harness{State: state}, calls: make(chan fake.Call, 8)}
	fake.Relay(t, relay, func(c fake.Call) fake.Reply { r.calls <- c; return fake.Reply{Stdout: "ok\n"} })
	r.h = &fake.Herdr{Home: t.TempDir(), Programs: map[string]string{"claude": fake.Beesfake(t)},
		Env: []string{"PATH=" + os.Getenv("PATH"), "BEESFAKE_RELAY=" + relay}}
	t.Cleanup(r.h.Close)
	return r
}

func (r rig) client(session string) herdr.Client { return herdr.Client{Run: r.h, Session: session} }

// start opens a workspace on a temp dir and starts beesfake there as name.
func (r rig) start(t *testing.T, session, name string) herdr.Workspace {
	t.Helper()
	c := r.client(session)
	ws, err := c.CreateWorkspace(context.Background(), t.TempDir(), "toy · "+name)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := r.harn.Prepare(port.Start{Name: name})
	if err := c.StartAgent(context.Background(), ws.Pane, name, "claude", l.Args...); err != nil {
		t.Fatal(err)
	}
	return ws
}

// Control characters are refused before the pane is looked up (probed, bees-1.14), and
// an unknown kind is a usage error, exit 2 (probed, 0.9.5).
func TestFakeHerdrRefusesWhatHerdrRefuses(t *testing.T) {
	r := newRig(t)
	_, err := r.h.Run(context.Background(), "", "herdr", "agent", "start", "x", "--kind", "claude", "--pane", "w9:p9", "--", "a\nb")
	var ee *port.ExitError
	if !errors.As(err, &ee) || !strings.Contains(string(ee.Stdout), "invalid_agent_argument") {
		t.Fatalf("control character: %v", err)
	}
	_, err = r.h.Run(context.Background(), "", "herdr", "agent", "start", "x", "--kind", "beesfake", "--pane", "w9:p9")
	if !errors.As(err, &ee) || ee.Code != 2 {
		t.Fatalf("unknown kind: %v", err)
	}
	if err := r.client("").StartAgent(context.Background(), "w9:p9", "x", "claude"); !errors.Is(err, herdr.ErrPaneGone) {
		t.Fatalf("missing pane: %v", err)
	}
}

// A --cwd that doesn't exist opens $HOME with exit 0 (probed, 0.9.5).
func TestFakeHerdrOpensAMissingCwdAsHome(t *testing.T) {
	r := newRig(t)
	ws, err := r.client("").CreateWorkspace(context.Background(), filepath.Join(t.TempDir(), "gone"), "x")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := r.h.Run(context.Background(), "", "herdr", "pane", "list", "--workspace", ws.ID)
	if !strings.Contains(string(out), `"cwd":"`+r.h.Home+`"`) {
		t.Fatalf("pane list: %s", out)
	}
}

// Token values are cut at 80 without a word (bees-12.17); --source may come either side
// of --token on 0.9.5 (probed). The client refuses a value herdr would cut.
func TestFakeHerdrCutsTokensAt80(t *testing.T) {
	r := newRig(t)
	ws, _ := r.client("").CreateWorkspace(context.Background(), t.TempDir(), "x")
	long := strings.Repeat("x", 100)
	if _, err := r.h.Run(context.Background(), "", "herdr", "workspace", "report-metadata", ws.ID, "--source", "bs", "--token", "worker="+long); err != nil {
		t.Fatal(err)
	}
	out, _ := r.h.Run(context.Background(), "", "herdr", "workspace", "get", ws.ID)
	if !strings.Contains(string(out), `"worker":"`+long[:80]+`"`) {
		t.Fatalf("workspace get: %s", out)
	}
	if err := r.client("").TagWorkspace(context.Background(), ws.ID, map[string]string{"worker": long}); err == nil {
		t.Fatal("the client sent a token herdr would cut")
	}
}

// Each server numbers from w1:p1 (probed: a herdr --session server does), and a pane
// process has HERDR_SESSION only on a named server.
func TestFakeHerdrPaneIdsArePerServer(t *testing.T) {
	r := newRig(t)
	a := r.start(t, "default", "toy-1")
	b := r.start(t, "toy", "toy-2")
	if a.Pane != "w1:p1" || b.Pane != "w1:p1" {
		t.Fatalf("panes %s and %s, want w1:p1 on each server", a.Pane, b.Pane)
	}
	env := map[string][]string{}
	for _, name := range []string{"toy-1", "toy-2"} {
		r.harn.Step(t, name, "bs", "inbox")
		c := <-r.calls
		env[name] = c.Env
		if res := r.harn.Await(t, name, 1); res.Code != 0 || res.Stdout != "ok\n" {
			t.Fatalf("%s's relayed call: %+v", name, res)
		}
	}
	if slices.ContainsFunc(env["toy-1"], func(e string) bool { return strings.HasPrefix(e, "HERDR_SESSION=") }) {
		t.Fatalf("default server set HERDR_SESSION: %v", env["toy-1"])
	}
	if !slices.Contains(env["toy-2"], "HERDR_SESSION=toy") || !slices.Contains(env["toy-2"], "HERDR_PANE_ID=w1:p1") {
		t.Fatalf("toy server env: %v", env["toy-2"])
	}
}

// agent_name_taken: a live agent has the name, and herdr starts nothing; a pane not yet
// at its shell is agent_pane_busy, typing nothing (bees-12.33).
func TestFakeHerdrRefusesATakenNameAndABusyPane(t *testing.T) {
	r := newRig(t)
	r.start(t, "", "toy-1")
	c := r.client("")
	ws, _ := c.CreateWorkspace(context.Background(), t.TempDir(), "x")
	l, _ := r.harn.Prepare(port.Start{Name: "toy-1"})
	if err := c.StartAgent(context.Background(), ws.Pane, "toy-1", "claude", l.Args...); !errors.Is(err, herdr.ErrNameTaken) {
		t.Fatalf("taken name: %v", err)
	}
	r.h.Busy = 1
	l, _ = r.harn.Prepare(port.Start{Name: "toy-2"})
	if err := c.StartAgent(context.Background(), ws.Pane, "toy-2", "claude", l.Args...); !errors.Is(err, herdr.ErrPaneBusy) {
		t.Fatalf("busy pane: %v", err)
	}
	if _, ok := r.h.Agent("default", "toy-2"); ok {
		t.Fatal("a busy pane started something")
	}
	if err := c.StartAgent(context.Background(), ws.Pane, "toy-2", "claude", l.Args...); err != nil {
		t.Fatalf("second try: %v", err)
	}
}

// A start can time out, or lose its terminal, while its process runs (bees-12.49).
func TestFakeHerdrStartFailsWhileTheProcessRuns(t *testing.T) {
	for _, mode := range []string{"timeout", "lost"} {
		r := newRig(t)
		c := r.client("")
		ws, _ := c.CreateWorkspace(context.Background(), t.TempDir(), "x")
		r.h.StartFails = mode
		l, _ := r.harn.Prepare(port.Start{Name: "toy-1"})
		err := c.StartAgent(context.Background(), ws.Pane, "toy-1", "claude", l.Args...)
		if err == nil || mode == "timeout" && !errors.Is(err, herdr.ErrUnready) {
			t.Fatalf("%s: err = %v", mode, err)
		}
		pi, err := c.ProcessInfo(context.Background(), ws.Pane)
		if err != nil || len(pi.Foreground) != 1 || pi.Foreground[0].Name != "claude" {
			t.Fatalf("%s: process-info = %+v, %v", mode, pi, err)
		}
	}
}

// pane move gives the pane a new id in the target workspace, keeps its process, and
// closes the emptied workspace (probed, 0.9.5); closing the pane hangs up its process.
func TestFakeHerdrPaneMoveRenumbersAndCloses(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.start(t, "", "toy-1")
	b, _ := r.client("").CreateWorkspace(ctx, t.TempDir(), "other")
	pid, _ := r.h.Agent("default", "toy-1")
	if _, err := r.h.Run(ctx, "", "herdr", "pane", "move", a.Pane, "--new-tab", "--workspace", b.ID, "--no-focus"); err != nil {
		t.Fatal(err)
	}
	panes, _ := r.client("").Panes(ctx, "")
	if !slices.Equal(panes, []string{b.Pane, b.ID + ":p2"}) {
		t.Fatalf("panes after move: %v", panes)
	}
	if gone, _ := r.client("").Panes(ctx, a.ID); len(gone) != 0 {
		t.Fatalf("emptied workspace kept %v", gone)
	}
	st, _ := proc.Start(pid)
	if ok, err := r.h.Alive(proc.Process{PID: pid, Start: st}); !ok || err != nil {
		t.Fatalf("moved process: %v, %v", ok, err)
	}
	if err := r.client("").ClosePane(ctx, b.ID+":p2"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.h.Alive(proc.Process{PID: pid, Start: st}); ok {
		t.Fatal("closing the pane left its process running")
	}
	if err := r.client("").ClosePane(ctx, b.ID+":p2"); err != nil {
		t.Fatalf("closing a closed pane: %v", err)
	}
	// --new-workspace makes the next workspace number, the pane its p1 (probed: w2:p1 → w4:p1
	// after w3).
	out, err := r.h.Run(ctx, "", "herdr", "pane", "move", b.Pane, "--new-workspace", "--label", "moved", "--no-focus")
	if err != nil || !strings.Contains(string(out), `"previous_pane_id":"`+b.Pane+`"`) || !strings.Contains(string(out), `"pane_id":"w3:p1"`) {
		t.Fatalf("move to a new workspace: %s, %v", out, err)
	}
}

// A ring reaches beesfake through its 0600 socket; no listener is ErrNotRunning.
func TestBeesfakeTakesRingsOnA0600Socket(t *testing.T) {
	r := newRig(t)
	r.start(t, "", "toy-1")
	fi, err := os.Stat(filepath.Join(r.harn.Dir("toy-1"), "ring.sock"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v, %v", fi, err)
	}
	if err := r.harn.Ring(port.Target{Name: "toy-1"}, "you have mail", port.Next); err != nil {
		t.Fatal(err)
	}
	var rings []string
	for deadline := time.Now().Add(5 * time.Second); len(rings) == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		rings = r.harn.Rings("toy-1")
	}
	if len(rings) != 1 || rings[0] != "next\tyou have mail" {
		t.Fatalf("rings = %q", rings)
	}
	if err := r.harn.Ring(port.Target{Name: "toy-9"}, "x", port.Now); !errors.Is(err, port.ErrNotRunning) {
		t.Fatalf("ring of nobody: %v", err)
	}
}
