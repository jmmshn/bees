package scenario_test

import (
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/fake"
	"github.com/jmmshn/bees/internal/harness/agy"
	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/port"
)

// agyRung is beesfake run and rung as an agy session is: herdr's --kind agy, and agy's
// Ring through herdr (bees-13.97).
type agyRung struct {
	fake.Harness
	agy agy.Adapter
}

func (agyRung) Kind() string { return agy.Name }

func (h agyRung) Ring(t port.Target, text string, p port.Priority) error {
	return h.agy.Ring(t, text, p)
}

func agyToy(t *testing.T) (*toy, string) {
	x := newToy(t)
	x.herdr.Programs[agy.Name] = fake.Beesfake(t)
	x.ports.Harness = agyRung{x.harness, agy.Adapter{Run: x.herdr}}
	return x, x.spawn("toy-1", "verdict").Herdr.Pane
}

// bs send to an agy worker says truthfully what its ring did (motif 11): nothing typed
// while it works, is blocked, or its box holds someone's text, each naming bs ring for
// after; typed once it is idle with an empty box, the doorbell counting all its unread
// mail. Never "delivered": herdr's ok means keys sent, not read.
func TestSendToAnAgyWorkerSaysHowItRang(t *testing.T) {
	x, pane := agyToy(t)
	for _, c := range []struct {
		sc   fake.Screen
		want string
	}{
		{fake.Screen{Status: herdr.Working}, "stored, not rung: toy-1 is working; if it's still unread once toy-1 is idle, run: bs ring toy-1"},
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

// A ring held at a permission menu, whose turn the keeper then cancels, leaves the
// worker idle with mail unread and no Stop hook to show it (bees-13.94): bs status calls
// it stuck and names bs ring, which types the doorbell once. bs ring holds again while it
// is busy (ExitHeld) and refuses once the mail is read.
func TestAHeldRingLeftIdleIsStuckUntilBsRing(t *testing.T) {
	x, pane := agyToy(t)
	must(t, x.worker("toy-1", 1, "inbox"), 0) // the brief, read
	x.herdr.SetScreen(x.session, pane, fake.Screen{Status: herdr.Blocked, Menu: true})
	must(t, x.queen("send", "toy-1", "hi"), 0, "stored, not rung: toy-1 is blocked")
	if r := x.queen("status"); strings.Contains(r.out, "stuck: toy-1") {
		t.Fatalf("a blocked worker called stuck:\n%s", r)
	}
	must(t, x.queen("ring", "toy-1"), 13, "not rung: toy-1 is blocked", "next: bs ring toy-1")
	x.herdr.SetScreen(x.session, pane, fake.Screen{Status: herdr.Done}) // Esc: the turn is cancelled, no Stop hook runs
	must(t, x.queen("status"), 0, "stuck: toy-1: idle with 1 unread, not rung → bs ring toy-1")
	if p := x.herdr.Prompts(x.session, pane); len(p) != 0 {
		t.Fatalf("typed before bs ring: %q", p)
	}
	must(t, x.queen("ring", "toy-1"), 0, "rung toy-1 (1 unread)")
	if p := x.herdr.Prompts(x.session, pane); len(p) != 1 || !strings.HasPrefix(p[0], "[bs] 1 unread (queen-toy)") {
		t.Fatalf("typed %q", p)
	}
	must(t, x.worker("toy-1", 2, "inbox"), 0, "hi")
	if r := x.queen("status"); strings.Contains(r.out, "stuck: toy-1") {
		t.Fatalf("stuck with its mail read:\n%s", r)
	}
	must(t, x.queen("ring", "toy-1"), 2, "toy-1 has no unread mail: nothing to ring")
}
