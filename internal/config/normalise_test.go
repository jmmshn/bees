package config_test

import (
	"testing"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/project"
)

// config repeats project's name rule; a project file named one way and found another
// would be a misnamed file nobody reads.
func TestNormaliseMatchesProject(t *testing.T) {
	for _, s := range []string{"bees", "Jobflow-Ray", "pymatgen_analysis-defects", "A_b-C"} {
		if got, want := config.Normalise(s), project.Normalise(s); got != want {
			t.Errorf("Normalise(%q) = %q, project says %q", s, got, want)
		}
	}
}
