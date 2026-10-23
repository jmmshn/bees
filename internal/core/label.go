package core

import (
	"errors"
	"fmt"
	"regexp"
)

// LabelRules says what a worker's label must be. The queen writes the label; bs only checks it.
const LabelRules = `a label names the work for the herdr sidebar: 1-4 lowercase words of a-z and 0-9, single spaces, no punctuation, at most 32 characters, specific to the bead.
  good: "role prompts", "squash merge end", "natural workspace labels"
  bad:  "fix bug", "update docs", "Role-Prompts."`

// ErrBadLabel is a label that breaks LabelRules.
var ErrBadLabel = errors.New("not a valid label")

var labelRule = regexp.MustCompile(`^[a-z0-9]+( [a-z0-9]+){0,3}$`)

// CheckLabel refuses a label that breaks LabelRules; it never rewrites one.
func CheckLabel(label string) error {
	if len(label) > 32 || !labelRule.MatchString(label) {
		return fmt.Errorf("%q: %w\n%s", label, ErrBadLabel, LabelRules)
	}
	return nil
}
