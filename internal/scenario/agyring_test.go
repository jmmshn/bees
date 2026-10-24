package scenario_test

import (
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/fake"
	"github.com/jmmshn/bees/internal/harness/agy"
	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/port"
)

// agyRung is beesfake rung as an agy session is: agy's Ring, through herdr (bees-13.97).
type agyRung struct {
	fake.Harness
	agy agy.Adapter
}

func (h agyRung) Ring(t port.Target, text string, p port.Priority) error {
	return h.agy.Ring(t, text, p)
}

// bs send to an agy worker says truthfully what its ring did (motif 11): nothing typed
// while it works, is blocked, or its box holds someone's text, each saying when it sees
// the mail; typed once it is idle with an empty box, the doorbell counting all its unread
// mail. Never "delivered": herdr's ok means keys sent, not read.
func TestSendToAnAgyWorkerSaysHowItRang(t *testing.T) {
	x := newToy(t)
	x.ports.Harness = agyRung{x.harness, agy.Adapter{Run: x.herdr}}
	sp := x.spawn("toy-1", "verdict")
	pane := sp.Herdr.Pane
	for _, c := range []struct {
		sc   fake.Screen
		want string
	}{
		{fake.Screen{Status: herdr.Working}, "stored, not rung: toy-1 is working; it sees the mail when this turn ends (its Stop hook) or at its next bs inbox"},
		{fake.Screen{Status: herdr.Working, Menu: true}, "stored, not rung: toy-1 is working;"},
		{fake.Screen{Status: herdr.Blocked, Menu: true}, "stored, not rung: toy-1 is blocked;"},
		{fake.Screen{Status: herdr.Idle, Box: "keeper half typed"}, "stored, not rung: toy-1's input box has text (someone is typing)"},
	} {
		x.herdr.SetScreen(x.session, pane, c.sc)
		r := x.queen("send", "toy-1", "hi")
		must(t, r, 0, c.want)
		if strings.Contains(r.out, "deliver") {
			t.Errorf("%+v: %s", c.sc, r.out)
		}
	}
	if p := x.herdr.Prompts(x.session, pane); len(p) != 0 {
		t.Fatalf("typed into a busy pane: %q", p)
	}
	x.herdr.SetScreen(x.session, pane, fake.Screen{Status: herdr.Idle})
	must(t, x.queen("send", "toy-1", "hi"), 0, "rung toy-1")
	if p := x.herdr.Prompts(x.session, pane); len(p) != 1 || !strings.HasPrefix(p[0], "[bs] 6 unread (queen-toy ×6) — run: bs inbox") { // the brief and five sends
		t.Fatalf("typed %q", p)
	}
}
