package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// wantErr fails unless err's text contains every part.
func wantErr(t *testing.T, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want one containing %q", parts)
	}
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error %q lacks %q", err, p)
		}
	}
}

func getenv(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

func TestDirPerSession(t *testing.T) {
	for _, c := range []struct {
		env          []string
		dir, session string
		err          string
	}{
		{env: []string{"HOME=/h"}, dir: "/h/.config/bees", session: "default"},
		{env: []string{"HOME=/h", "HERDR_SESSION=default"}, dir: "/h/.config/bees", session: "default"},
		{env: []string{"HOME=/h", "HERDR_SESSION=lt1"}, dir: "/h/.config/bees/sessions/lt1", session: "lt1"},
		{env: []string{"HOME=/h", "XDG_CONFIG_HOME=/x", "HERDR_SESSION=lt1"}, dir: "/x/bees/sessions/lt1", session: "lt1"},
		{env: []string{"HOME=/h", "XDG_CONFIG_HOME=x"}, err: "not an absolute path"},
		{env: []string{"HOME=/h", "HERDR_SESSION=../x"}, err: "can't name a config dir"},
		{env: nil, err: "neither"},
	} {
		dir, session, err := Dir(func(k string) string { return getenv(c.env, k) })
		if c.err != "" {
			wantErr(t, err, c.err)
			continue
		}
		if err != nil || dir != c.dir || session != c.session {
			t.Errorf("Dir(%q) = %q, %q, %v; want %q, %q", c.env, dir, session, err, c.dir, c.session)
		}
	}
}

func TestDefaultsAndSources(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.toml"), "[paths]\nlogbook_dir = \"~/lb\"\n[worker.claude_code]\nmodel = \"opus\"\n")
	write(t, filepath.Join(dir, "projects", "toy.toml"), "[paths]\nrepo = \"/r/toy\"\n[worker.claude_code]\nmodel = \"sonnet\"\n")
	l, err := Load(Input{Dir: dir, Project: "toy", Home: "/h"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Harness != "claude_code" || l.Paths.Worktrees != "{repo}-WT" || l.Worker.ClaudeCode.Model != "sonnet" || l.Expand(l.Paths.LogbookDir) != "/h/lb" {
		t.Errorf("config = %+v", l.Config)
	}
	for key, want := range map[string]string{
		"harness": "default", "paths.logbook_dir": "global " + filepath.Join(dir, "config.toml"),
		"worker.claude_code.model": "project " + filepath.Join(dir, "projects", "toy.toml"),
	} {
		if got := l.Sources[key].String(); got != want {
			t.Errorf("%s from %q, want %q", key, got, want)
		}
	}
	if !l.Files[0].Exists || !l.Files[1].Exists {
		t.Errorf("files = %+v", l.Files)
	}
}

func TestEnvOverridesFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.toml"), "[paths]\nlogbook_dir = \"/a\"\n")
	l, err := Load(Input{Dir: dir, Home: "/h", Env: map[string]string{"BS_LOGBOOK_DIR": "/b"}})
	if err != nil || l.Paths.LogbookDir != "/b" || l.Sources["paths.logbook_dir"].String() != "env BS_LOGBOOK_DIR" {
		t.Fatalf("%+v %v", l, err)
	}
	_, err = Load(Input{Dir: dir, Home: "/h", Env: map[string]string{"BS_LOGBOOK_DIR": "rel"}})
	wantErr(t, err, "env BS_LOGBOOK_DIR", "not an absolute path")
}

// Every error in both files is reported at once, each naming its file and key.
func TestErrorsNameFileAndKey(t *testing.T) {
	dir := t.TempDir()
	global, proj := filepath.Join(dir, "config.toml"), filepath.Join(dir, "projects", "toy.toml")
	write(t, global, "harness = \"agy\"\nbogus = 1\n[paths]\nrepo = \"/r\"\n[workspace]\nkind = \"nfs\"\n")
	write(t, proj, "harness = \"claude_code\"\n[paths]\nlogbook_dir = \"/x\"\nworktrees = \"/a/{repo}\"\n[worker]\npath = [\"\"]\n")
	_, err := Load(Input{Dir: dir, Project: "toy", Home: "/h"})
	wantErr(t, err,
		global+`: harness = "agy": must be one of claude_code`,
		global+": unknown key bogus",
		global+": paths.repo belongs in the project file, not the global one",
		global+`: workspace.kind = "nfs"`,
		proj+": harness belongs in the global file",
		proj+": paths.logbook_dir belongs in the global file",
		proj+`: paths.worktrees = "/a/{repo}"`,
		proj+": worker.path: an empty item")
}

func TestTables(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.toml"), `
[workspace.command]
create = ["jj", "workspace", "add", "{dir}"]
remove = ["jj", "workspace", "forget", "{name}"]
clean = ["sh", "-c", "true"]
dir = "{repo}-WS/{name}"
timeout = "60s"

[checks.cl_submitted]
run = ["cl", "status", "{cl}"]

[goal.cl]
handoff = ["clean", "cmd:cl_submitted"]
complete = ["cmd:cl_submitted"]
`)
	write(t, filepath.Join(dir, "projects", "toy.toml"), `
[workspace.command]
create = ["hg", "share", "{dir}"]
remove = ["rm", "-r", "{dir}"]
clean = ["hg", "status"]
dir = "/w/{name}"

[guards]
deny = ["git push *"]
replace = true
`)
	l, err := Load(Input{Dir: dir, Project: "toy", Home: "/h"})
	if err != nil {
		t.Fatal(err)
	}
	// The nearest file sets a table whole: no create from one file, remove from another.
	if c := l.Workspace.Command; c == nil || c.Create[0] != "hg" || c.Timeout != "" {
		t.Errorf("workspace.command = %+v", c)
	}
	if k, _ := l.Kind(true); k != CommandKind || l.Goal["cl"].Handoff[1] != "cmd:cl_submitted" || !l.Guards.Replace {
		t.Errorf("config = %+v", l.Config)
	}
	var keys []string
	for _, f := range l.Fields() {
		keys = append(keys, f.Key+"="+f.Value)
	}
	all := strings.Join(keys, "\n")
	for _, want := range []string{`goal.cl.handoff=["clean", "cmd:cl_submitted"]`, `checks.cl_submitted.run=["cl", "status", "{cl}"]`,
		`workspace.command.dir="/w/{name}"`, "guards.replace=true"} {
		if !strings.Contains(all, want) {
			t.Errorf("fields lack %s:\n%s", want, all)
		}
	}
}

func TestTableErrors(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	write(t, global, `
[workspace.command]
create = ["jj"]
remove = ["jj"]
dir = "x"
timeout = "soon"

[checks.empty]
run = []

[goal.pr]
handoff = ["clean"]

[goal.cl]
handoff = ["cmd:nope", "made_up"]
`)
	_, err := Load(Input{Dir: dir, Home: "/h"})
	wantErr(t, err, `[workspace.command] timeout = "soon"`,
		"[checks.empty] run: an argv is required", "[goal.pr] pr is a built-in goal type",
		`[goal.cl] handoff: "cmd:nope" names no [checks.nope]`, `"made_up" is no built-in check`)
}

// A [workspace.command] missing a key loads, so bs config and doctor can show it; spawn
// refuses it.
func TestCommandMissingLoads(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.toml"), "[workspace.command]\ncreate = [\"jj\"]\n")
	l, err := Load(Input{Dir: dir, Home: "/h"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Workspace.Command.Missing(), " "); got != "remove clean dir" {
		t.Errorf("missing = %q", got)
	}
}

func TestKind(t *testing.T) {
	l := &Loaded{Sources: map[string]Source{}}
	if k, _ := l.Kind(true); k != GitWorktree {
		t.Error("in a git checkout:", k)
	}
	if k, _ := l.Kind(false); k != NoWorkspace {
		t.Error("outside one:", k)
	}
	l.Workspace.Kind = NoWorkspace
	if k, why := l.Kind(true); k != NoWorkspace || !strings.HasPrefix(why, "workspace.kind") {
		t.Error("override:", k, why)
	}
}

// A project file spelled another way is refused: bs would never read it (ADR-0008).
func TestMisnamedProjectFile(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "projects", "jobflow-ray.toml"), "")
	_, err := Load(Input{Dir: dir, Project: "jobflowray", Home: "/h"})
	wantErr(t, err, "jobflow-ray.toml: bs never reads it; rename it to "+filepath.Join(dir, "projects", "jobflowray.toml"))
}

func TestQuote(t *testing.T) {
	if got := Quote("a\"b\\c\n"); got != `"a\"b\\c\u000A"` {
		t.Error(got)
	}
}
