package prompt_test

import (
	"os"
	"path/filepath"
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

func TestFileHoldsTheSystemPromptInBsCache(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	for range 2 { // rewritten, not appended, at every launch
		path, err := prompt.File("web2")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(cache, "bees", "web2-system.md"); path != want {
			t.Fatalf("path = %q, want %q", path, want)
		}
		if b, err := os.ReadFile(path); err != nil || string(b) != prompt.System("web2") {
			t.Fatalf("%s holds %q, %v; want System(web2)", path, b, err)
		}
	}
}
