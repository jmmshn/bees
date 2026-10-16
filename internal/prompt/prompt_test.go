package prompt_test

import (
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/prompt"
)

func TestSystemNamesTheProjectEverywhere(t *testing.T) {
	got := prompt.System("web2")
	for _, want := range []string{"web2", "bs bead ls", "bs epic", "bs bead new --parent web2-N"} {
		if !strings.Contains(got, want) {
			t.Errorf("system prompt lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "{{") {
		t.Errorf("unfilled placeholder:\n%s", got)
	}
}
