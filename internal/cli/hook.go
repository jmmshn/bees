package cli

import (
	"flag"
	"fmt"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/harness/agy"
	"github.com/jmmshn/bees/internal/watch"
)

// Arm is what agy's PreInvocation hook tells a session with no mail watcher at the start
// of a turn: the watcher's exit is its doorbell (bees-13.13.10).
var Arm = "[" + bsname.Cmd + "] Run this command in the background, without waiting for it, then carry on: " +
	bsname.Cmd + " inbox --wait   (it exits when new mail comes, which wakes you; then run " + bsname.Cmd + " inbox)"

// hook is agy's PreInvocation and Stop hook (bees-13.13.10), run by the hooks.json its
// adapter renders, from the session's workspace, with agy's payload on stdin. At a turn's
// first model call with no watcher armed, it injects Arm. When a turn ends on its own
// with unread mail, it holds the turn with the doorbell: mail that came mid-turn, or that
// woke it, is read before it idles. Anything else, and any caller not a queen or worker
// (the keeper's own agy), prints nothing: agy's no-op. An error prints nothing to stdout
// either: the mail stays in the logbook, read at the next bs inbox.
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
	if event == "PreInvocation" {
		first, err := agy.FirstCall(r.env.Stdin)
		if err != nil || !first {
			return err
		}
		if _, armed, err := watch.Armed(watch.Path(r.ports.CacheDir, w.Name)); err != nil || armed {
			return err
		}
		return agy.Inject(r.out, Arm)
	}
	ended, err := agy.Stopped(r.env.Stdin)
	if err != nil || !ended {
		return err
	}
	ms, err := r.c.Logbook.Unread(r.ctx, w.Name)
	if err != nil || len(ms) == 0 {
		return err
	}
	return agy.Hold(r.out, core.Doorbell(ms, ms[len(ms)-1].From))
}
