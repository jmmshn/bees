package core_test

import (
	"testing"

	"github.com/jmmshn/bees/internal/core"
	"github.com/jmmshn/bees/internal/logbook"
)

// The doorbell names senders and counts, never content, and the reply command: across
// projects with --project, queen-<project> when the sender is the keeper (bees-13.78.7),
// and none when senders span projects (bees-12.41).
func TestDoorbell(t *testing.T) {
	m := func(from, project string) logbook.Message { return logbook.Message{From: from, FromProject: project} }
	for _, c := range []struct {
		unread []logbook.Message
		from   string
		want   string
	}{
		{[]logbook.Message{m("queen-bees", ""), m("bees-1", ""), m("queen-bees", "")}, "queen-bees",
			`[bs] 3 unread (queen-bees ×2, bees-1) — run: bs inbox   (reply with: bs send queen-bees "...")`},
		{[]logbook.Message{m("keeper", ""), m("keeper", ""), m("keeper", "")}, "keeper",
			`[bs] 3 unread (keeper ×3) — run: bs inbox   (reply with: bs send queen-bees "...")`},
		{[]logbook.Message{m("queen-dot", "dot")}, "queen-dot",
			`[bs] 1 unread (queen-dot) — run: bs inbox   (reply with: bs send --project dot queen-dot "...")`},
		{[]logbook.Message{m("keeper", "dot")}, "keeper",
			`[bs] 1 unread (keeper) — run: bs inbox   (reply with: bs send --project dot queen-dot "...")`},
		{[]logbook.Message{m("queen-dot", "dot"), m("bees-1", "")}, "bees-1",
			`[bs] 2 unread (queen-dot, bees-1) — run: bs inbox   (it shows how to reply to each)`},
	} {
		if got := core.Doorbell(c.unread, c.from, "bees"); got != c.want {
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

func TestBeadOwner(t *testing.T) {
	for bead, want := range map[string]string{
		"bees-13.73.3": "bees", "dotfiles-3.1": "dotfiles", "bees-1": "bees", "queen-bees": "", "bees-x": "", "bees-1.": "", "bees": "",
	} {
		if got := core.BeadOwner(bead); got != want {
			t.Errorf("BeadOwner(%q) = %q, want %q", bead, got, want)
		}
	}
}
