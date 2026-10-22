package port_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jmmshn/bees/internal/port"
)

type runner func(argv ...string) ([]byte, error)

func (f runner) Run(_ context.Context, _ string, argv ...string) ([]byte, error) { return f(argv...) }

// Only bd's own "no issues found" is no such bead; a store bd can't open is bd not
// answering, which spawn reads as "can't check", never "no" (Rule G).
func TestBeadsShowTellsNoSuchBeadFromBdFailing(t *testing.T) {
	for _, c := range []struct {
		name   string
		stdout string
		stderr string
		noBead bool
	}{
		{"missing", `{"error": "no issues found matching the provided IDs", "schema_version": 1}`, `Error fetching x-9: no issue found matching "x-9"`, true}, // bd 1.x, probed
		{"locked", "", "Error: database is locked", false},
	} {
		b := port.BeadsCLI{R: runner(func(argv ...string) ([]byte, error) {
			return nil, &port.ExitError{Argv: argv, Code: 1, Stdout: []byte(c.stdout), Stderr: []byte(c.stderr)}
		})}
		_, err := b.Show(context.Background(), "x-9")
		if err == nil || errors.Is(err, port.ErrNoBead) != c.noBead {
			t.Errorf("%s: Show = %v; want no such bead: %v", c.name, err, c.noBead)
		}
	}
}
