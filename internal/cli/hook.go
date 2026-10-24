package cli

import (
	"flag"
	"fmt"

	"github.com/jmmshn/bees/internal/bsname"
	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/harness/agy"
)

// hook is agy's Stop hook, run by the hooks.json its adapter renders, from the session's
// workspace, with agy's payload on stdin. When a turn ends on its own with mail unread
// (mail that came mid-turn, which agy's ring doesn't type into a working session:
// bees-13.97), it holds the turn with the doorbell. Anything else, and any caller not a
// queen or worker (the keeper's own agy), prints nothing: agy's no-op. An error prints
// nothing to stdout either, and agy 1.4.6 lets a turn go on past a hook that exits
// non-zero with no stdout (probed): the mail stays in the logbook, read at the next bs
// inbox.
func (r *run) hook(fs *flag.FlagSet, args []string) error {
	harness := fs.String("harness", "", "the harness whose hook this is: agy")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *harness != agy.Name || fs.NArg() != 1 || fs.Arg(0) != "Stop" {
		return fmt.Errorf("%w: "+bsname.Cmd+" hook --harness agy Stop", errUsage)
	}
	w := r.caller
	if w.Role != core.WorkerR && w.Role != core.QueenR {
		return nil
	}
	ended, err := agy.Stopped(r.env.Stdin)
	if err != nil || !ended {
		return err
	}
	ms, err := r.c.Logbook.Unread(r.ctx, w.Name)
	if err != nil || len(ms) == 0 {
		return err
	}
	return agy.Hold(r.out, core.Doorbell(ms, ms[len(ms)-1].From, r.c.Project))
}
