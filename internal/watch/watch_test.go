package watch

import (
	"path/filepath"
	"testing"
)

// One watcher per session: a second Arm gets the first's pid and no lock; once the first
// lets go, nothing is armed and the next Arm takes it.
func TestOneWatcherPerSession(t *testing.T) {
	path := Path(t.TempDir(), "toy-1")
	if _, armed, err := Armed(path); err != nil || armed {
		t.Fatalf("no lock file: armed %v, %v; want none", armed, err)
	}
	release, held, err := Arm(path, 4242)
	if err != nil || release == nil || held != 0 {
		t.Fatalf("first Arm: release %v, held %d, %v", release != nil, held, err)
	}
	if pid, armed, err := Armed(path); err != nil || !armed || pid != 4242 {
		t.Fatalf("Armed = %d %v %v; want 4242 true", pid, armed, err)
	}
	if r2, held, err := Arm(path, 5151); err != nil || r2 != nil || held != 4242 {
		t.Fatalf("second Arm: release %v, held %d, %v; want refused, held by 4242", r2 != nil, held, err)
	}
	release()
	if _, armed, err := Armed(path); err != nil || armed {
		t.Fatalf("after release: armed %v, %v", armed, err)
	}
	r3, held, err := Arm(path, 6262)
	if err != nil || r3 == nil || held != 0 {
		t.Fatalf("Arm after release: %v %d %v", r3 != nil, held, err)
	}
	r3()
}

func TestPathIsACacheFileOfTheSession(t *testing.T) {
	if got := filepath.Base(Path("/c", "toy-1")); got != "toy-1-watch-lock" {
		t.Fatalf("Path base = %q", got)
	}
}
