package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A written file loads as the defaults plus what init set, in each scope, and names
// every key the scope may hold; a key outside the scope is refused.
func TestTemplateLoads(t *testing.T) {
	for _, tc := range []struct {
		scope string
		set   map[string]string
		want  map[string]string
		holds []string
	}{
		{"global", map[string]string{"paths.logbook_dir": "/tmp/lb", "harness": "claude_code"},
			map[string]string{"paths.logbook_dir": "/tmp/lb", "harness": "claude_code"}, []string{"# repos = \"\"", "[guards]", "# [workspace.command]", "# [checks.cl_sent]", "(not read by bs yet: bees-13.11)", "logbook_dir = \"/tmp/lb\""}},
		{"project", map[string]string{"paths.repo": "/tmp/notes", "workspace.kind": "none"},
			map[string]string{"paths.repo": "/tmp/notes", "workspace.kind": "none"}, []string{"# path = []", "[worker]"}},
	} {
		text, err := Template(tc.scope, tc.set)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range tc.holds {
			if !strings.Contains(text, h) {
				t.Errorf("%s file lacks %q:\n%s", tc.scope, h, text)
			}
		}
		dir := t.TempDir()
		path, project := filepath.Join(dir, "config.toml"), ""
		if tc.scope == "project" {
			path, project = filepath.Join(dir, "projects", "toy.toml"), "toy"
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		l, err := Load(Input{Dir: dir, Project: project, Home: "/home/x"})
		if err != nil {
			t.Fatalf("%s file doesn't load: %v\n%s", tc.scope, err, text)
		}
		for _, f := range l.Fields() {
			src := l.Sources[f.Key]
			if v, ok := tc.want[f.Key]; ok != (src.Kind != "default") || ok && f.Value != Quote(v) {
				t.Errorf("%s: %s = %s from %s", tc.scope, f.Key, f.Value, src)
			}
		}
	}
	if _, err := Template("project", map[string]string{"harness": "claude_code"}); err == nil || !strings.Contains(err.Error(), "belongs in the global file") {
		t.Errorf("harness in a project file: %v", err)
	}
}

// Every unit table has an example: one without would be written as a bare comment.
func TestEveryTableHasAnExample(t *testing.T) {
	var c Config
	for _, f := range leaves(reflect.ValueOf(&c).Elem(), "") {
		if f.table() && examples[f.key] == "" {
			t.Errorf("[%s] has no example in config init's template", f.key)
		}
	}
}
