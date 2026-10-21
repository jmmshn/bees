package worker

import "time"

// Fast makes starts look for their harness process every millisecond and give up after
// wait, for tests; it returns a func that undoes it.
func Fast(wait time.Duration) func() {
	cw, we := confirmWait, watchEvery
	confirmWait, watchEvery = wait, time.Millisecond
	return func() { confirmWait, watchEvery = cw, we }
}
