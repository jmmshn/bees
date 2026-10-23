package guards

import "fmt"

// Verdict is bs guard's answer to one tool call (bees-13.13's permission seam).
type Verdict int

const (
	// None is no decision: the harness's own permission check decides, which on agy is
	// its prompt to the keeper.
	None Verdict = iota
	// Deny blocks the call.
	Deny
	// Allow runs the call unasked: bees-13.13.9's unattended worker. Decide never returns
	// it yet.
	Allow
)

// Call is one tool call a harness asks about. Command is a shell call's whole command
// line, and empty for any other tool.
type Call struct {
	Tool, Command string
}

// Worker is what Decide judges a worker's calls by: its deny globs (Build).
type Worker struct {
	Deny []string
}

// Decision is Decide's verdict and, for a deny, why.
type Decision struct {
	Verdict Verdict
	Reason  string
}

// Decide is the one policy over a worker's tool calls: a shell command a deny glob
// matches is denied, and everything else gets no decision. A call the caller couldn't
// read never reaches Decide: the caller denies it (fail closed).
func Decide(call Call, w Worker) Decision {
	if glob, ok := Denies(w.Deny, call.Command); ok && call.Command != "" {
		return Decision{Deny, fmt.Sprintf("the keeper's guards deny %q", glob)}
	}
	return Decision{Verdict: None}
}
