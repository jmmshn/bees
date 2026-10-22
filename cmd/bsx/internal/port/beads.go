package port

import (
	"context"
	"encoding/json"
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
}

func (b BeadsCLI) Show(ctx context.Context, id string) (Bead, error) {
	out, err := b.R.Run(ctx, "", "bd", "show", id, "--json")
	if err != nil {
		return Bead{}, fmt.Errorf("bd show %s: %w", id, err)
	}
	var bs []bdBead
	if err := json.Unmarshal(out, &bs); err != nil {
		return Bead{}, fmt.Errorf("bd show %s: %w", id, err)
	}
	if len(bs) == 0 {
		return Bead{}, fmt.Errorf("bd show %s: no such bead", id)
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
