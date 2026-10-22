package core_test

import (
	"testing"

	"github.com/jmmshn/bees/cmd/bsx/internal/core"
	"github.com/jmmshn/bees/cmd/bsx/internal/logbook"
)

// The doorbell names senders and counts, never content, and the reply command: across
// projects with --project, and none when senders span projects (bees-12.41).
func TestDoorbell(t *testing.T) {
	m := func(from, project string) logbook.Message { return logbook.Message{From: from, FromProject: project} }
	for _, c := range []struct {
		unread []logbook.Message
		from   string
		want   string
	}{
		{[]logbook.Message{m("queen-bees", ""), m("bees-1", ""), m("queen-bees", "")}, "queen-bees",
			`[bsx] 3 unread (queen-bees ×2, bees-1) — run: bsx inbox   (reply with: bsx send queen-bees "...")`},
		{[]logbook.Message{m("queen-dot", "dot")}, "queen-dot",
			`[bsx] 1 unread (queen-dot) — run: bsx inbox   (reply with: bsx send --project dot queen-dot "...")`},
		{[]logbook.Message{m("queen-dot", "dot"), m("bees-1", "")}, "bees-1",
			`[bsx] 2 unread (queen-dot, bees-1) — run: bsx inbox   (it shows how to reply to each)`},
	} {
		if got := core.Doorbell(c.unread, c.from); got != c.want {
			t.Errorf("Doorbell = %q\n want %q", got, c.want)
		}
	}
}

func TestOwner(t *testing.T) {
	for name, want := range map[string]string{
		"queen-bees": "bees", "bees-13-4": "bees", "bees-1": "bees", "keeper": "", "bees-x": "", "bees-": "", "-1": "",
	} {
		if got := core.Owner(name); got != want {
			t.Errorf("Owner(%q) = %q, want %q", name, got, want)
		}
	}
}
