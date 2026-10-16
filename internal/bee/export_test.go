package bee

import (
	"testing"
	"time"
)

// FastWatch shortens Watch's waits for a test.
func FastWatch(t *testing.T) {
	poll, grace, appear := watchPoll, exitGrace, appearWait
	watchPoll, exitGrace, appearWait = 5*time.Millisecond, 20*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { watchPoll, exitGrace, appearWait = poll, grace, appear })
}
