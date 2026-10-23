package port

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// BeadsCLI is the bead tracker through the bd command line, run by R.
type BeadsCLI struct{ R Runner }

type bdBead struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Notes       string `json:"notes"`
	Design      string `json:"design"`
	Parent      string `json:"parent"`
	Status      string `json:"status"`
}

func (b BeadsCLI) Show(ctx context.Context, id string) (Bead, error) {
	out, err := b.R.Run(ctx, "", "bd", "show", id, "--json")
	var ee *ExitError
	if errors.As(err, &ee) && ee.Code == 1 && bytes.Contains(ee.Stdout, []byte(`"no issues found`)) {
		return Bead{}, fmt.Errorf("bd show %s: %w", id, ErrNoBead) // bd 1.x: exit 1, {"error": "no issues found …"}
	}
	if err != nil {
		return Bead{}, fmt.Errorf("bd show %s: %w", id, err)
	}
	var bs []bdBead
	if err := json.Unmarshal(out, &bs); err != nil {
		return Bead{}, fmt.Errorf("bd show %s: %w", id, err)
	}
	if len(bs) == 0 {
		return Bead{}, fmt.Errorf("bd show %s: %w", id, ErrNoBead)
	}
	x := bs[0]
	return Bead(x), nil
}

func (b BeadsCLI) Children(ctx context.Context, parent string) ([]string, error) {
	out, err := b.R.Run(ctx, "", "bd", "list", "--all", "--parent", parent, "--limit", "0", "--json")
	if err != nil {
		return nil, fmt.Errorf("bd list --parent %s: %w", parent, err)
	}
	var bs []bdBead
	if err := json.Unmarshal(out, &bs); err != nil {
		return nil, fmt.Errorf("bd list --parent %s: %w", parent, err)
	}
	ids := make([]string, len(bs))
	for i, x := range bs {
		ids[i] = x.ID
	}
	return ids, nil
}
