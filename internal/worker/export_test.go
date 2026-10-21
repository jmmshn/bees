package worker

import "time"

// Fast makes starts look for their harness process, and retry a busy pane, every
// millisecond and give up after wait, for tests; it returns a func that undoes it.
func Fast(wait time.Duration) func() {
	cw, we, bw := confirmWait, watchEvery, busyWait
	confirmWait, watchEvery, busyWait = wait, time.Millisecond, wait
	return func() { confirmWait, watchEvery, busyWait = cw, we, bw }
}

// FastGate shortens the gate's second look to look, for tests; it returns a func that
// undoes it.
func FastGate(look time.Duration) func() {
	sl := secondLook
	secondLook = look
	return func() { secondLook = sl }
}
