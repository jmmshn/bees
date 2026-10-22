package fake_test

import (
	"os"
	"testing"

	"github.com/jmmshn/bees/cmd/bsx/internal/fake"
)

func TestMain(m *testing.M) {
	code := m.Run()
	fake.RemoveBuild()
	os.Exit(code)
}
