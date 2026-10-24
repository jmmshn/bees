package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/harness/agy"
	"github.com/jmmshn/bees/internal/watch"
)

// Arm is what agy's PreInvocation hook tells a session with no mail watcher at the start
// of a turn: the watcher's exit is its doorbell (bees-13.13.10).
var Arm = "[" + bsname.Cmd + "] Run this command in the background, without waiting for it, then carry on: " +
	bsname.Cmd + " inbox --wait   (it exits when new mail comes, which wakes you; then run " + bsname.Cmd + " inbox)"

// settle is how long agy's Stop hook waits for a watcher the turn just started in the
// background to take its lock, before holding the turn to arm one.
const settle = 2 * time.Second

// hook is agy's PreInvocation and Stop hook (bees-13.13.10), run by the hooks.json its
// adapter renders, from the session's workspace, with agy's payload on stdin. At a turn's
// first model call with no watcher armed, it injects Arm. When a turn ends on its own, it
// holds the turn: with the doorbell while mail is unread (mail that came mid-turn, or
// that woke it), else with Arm while no watcher is armed (one that exited mid-turn, its
// mail read in the same turn), so a session never idles deaf; that once per idle, so a
// session that ignores Arm isn't held at every stop. Anything else, and any
// caller not a queen or worker (the keeper's own agy), prints nothing: agy's no-op. An
// error prints nothing to stdout either, and agy lets a turn go on past a hook that exits
// non-zero with no stdout (probed on agy.Probed): the mail stays in the logbook, read at
// the next bs inbox.
func (r *run) hook(fs *flag.FlagSet, args []string) error {
	harness := fs.String("harness", "", "the harness whose hook this is: agy")
	if err := parse(fs, args); err != nil {
		return err
	}
	event := fs.Arg(0)
	if *harness != agy.Name || fs.NArg() != 1 || event != "PreInvocation" && event != "Stop" {
		return fmt.Errorf("%w: "+bsname.Cmd+" hook --harness agy PreInvocation|Stop", errUsage)
	}
	w := r.caller
	if w.Role != core.WorkerR && w.Role != core.QueenR {
		return nil
	}
	lock := watch.Path(r.ports.CacheDir, w.Name)
	if event == "PreInvocation" {
		first, err := agy.FirstCall(r.env.Stdin)
		if err != nil || !first {
			return err
		}
		if _, armed, err := watch.Armed(lock); err != nil || armed {
			return err
		}
		return agy.Inject(r.out, Arm)
	}
	ended, err := agy.Stopped(r.env.Stdin)
	if err != nil || !ended {
		return err
	}
	ms, err := r.c.Logbook.Unread(r.ctx, w.Name)
	if err != nil {
		return err
	}
	if len(ms) > 0 {
		return agy.Hold(r.out, core.Doorbell(ms, ms[len(ms)-1].From, r.c.Project))
	}
	asked := watch.Asked(r.ports.CacheDir, w.Name)
	if _, err := os.Stat(asked); err == nil || !errors.Is(err, os.ErrNotExist) {
		return err // asked once this idle already: let it stop
	}
	for deadline := time.Now().Add(settle); ; time.Sleep(100 * time.Millisecond) {
		if _, armed, err := watch.Armed(lock); err != nil || armed {
			return err
		}
		if !time.Now().Before(deadline) {
			break
		}
	}
	if err := os.WriteFile(asked, nil, 0o600); err != nil {
		return err
	}
	return agy.Hold(r.out, Arm)
}
