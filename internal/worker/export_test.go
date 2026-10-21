package worker

import "time"

// Fast makes starts look for their harness process every millisecond and give up after
// wait, for tests; it returns a func that undoes it.
func Fast(wait time.Duration) func() {
	cw, we := confirmWait, watchEvery
	confirmWait, watchEvery = wait, time.Millisecond
	return func() { confirmWait, watchEvery = cw, we }
}

// FastGate shortens the gate's second look to look, for tests; it returns a func that
// undoes it.
func FastGate(look time.Duration) func() {
	sl := secondLook
	secondLook = look
	return func() { secondLook = sl }
}
