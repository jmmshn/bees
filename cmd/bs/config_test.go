package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func configEnv(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"BS_PROJECT", "BS_LOGBOOK_DIR", "BS_REPOS"} {
		t.Setenv(k, "")
	}
	return filepath.Join(xdg, "bees")
}

// bs config's output is TOML an agent can copy: it parses, and every key is there.
func TestConfigPrintsEveryKeyAsTOMLWithSource(t *testing.T) {
	dir := configEnv(t)
	proj := filepath.Join(dir, "projects", "bees.toml")
	if err := os.MkdirAll(filepath.Dir(proj), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proj, []byte("[worker.claude_code]\nmodel = \"sonnet\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"config", "--project", "bees"}, strings.NewReader(""), &out, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	for _, want := range []string{
		`worker.claude_code.model = "sonnet"  # project ` + proj,
		`paths.logbook_dir = "~/.local/share/bees"  # default`,
		`paths.repos = ""  # default; global file only`,
		`# global file: ` + filepath.Join(dir, "config.toml") + ` (missing)`,
		`run on claude_code`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	var parsed map[string]any
	if _, err := toml.Decode(out.String(), &parsed); err != nil {
		t.Errorf("output is not TOML: %v", err)
	}
}

func TestConfigErrorFailsAndNamesTheFile(t *testing.T) {
	dir := configEnv(t)
	global := filepath.Join(dir, "config.toml")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("[paths]\nrepoz = \"/x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"config", "--project", "bees"}, strings.NewReader(""), &out, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if want := "bs config: " + global + ": unknown key paths.repoz"; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

// A value TOML can't hold verbatim still prints as TOML that decodes back to it.
func TestConfigQuotesValuesAsTOML(t *testing.T) {
	configEnv(t)
	want := "/odd\a\v\"q\\ dir"
	t.Setenv("BS_REPOS", want)
	var out, stderr bytes.Buffer
	if code := run([]string{"config", "--project", "bees"}, strings.NewReader(""), &out, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var parsed struct{ Paths struct{ Repos string } }
	if _, err := toml.Decode(out.String(), &parsed); err != nil || parsed.Paths.Repos != want {
		t.Errorf("paths.repos decodes to %q (%v), want %q:\n%s", parsed.Paths.Repos, err, want, out.String())
	}
}
