package config

import (
	"os"
	"path/filepath"
	"slices"
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

// Shadowed lists the values a setting held before the one that won, so bs can tell a moved
// paths.logbook_dir from a new one.
func TestShadowedListsTheLowerValues(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.toml"), "[paths]\nlogbook_dir = \"/g\"\n")
	l, err := Load(Input{Dir: dir, Home: "/h", Env: map[string]string{"BS_LOGBOOK_DIR": "/e"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := l.Shadowed["paths.logbook_dir"], []string{"~/.local/share/bees", "/g"}; !slices.Equal(got, want) {
		t.Errorf("shadowed = %q, want %q", got, want)
	}
}

// Every error in both files is reported at once, each naming its file and key.
func TestErrorsNameFileAndKey(t *testing.T) {
	dir := t.TempDir()
	global, proj := filepath.Join(dir, "config.toml"), filepath.Join(dir, "projects", "toy.toml")
	write(t, global, "harness = \"omp\"\nbogus = 1\n[paths]\nrepo = \"/r\"\n[workspace]\nkind = \"nfs\"\n")
	write(t, proj, "harness = \"claude_code\"\n[paths]\nlogbook_dir = \"/x\"\nworktrees = \"/a/{repo}\"\n[worker]\npath = [\"\"]\n")
	_, err := Load(Input{Dir: dir, Project: "toy", Home: "/h"})
	wantErr(t, err,
		global+`: harness = "omp": must be one of claude_code, agy`,
		global+": unknown key bogus",
		global+": paths.repo belongs in the project file, not the global one",
		global+`: workspace.kind = "nfs"`,
		proj+": harness belongs in the global file",
		proj+": paths.logbook_dir belongs in the global file",
		proj+`: paths.worktrees = "/a/{repo}"`,
		proj+": worker.path: an empty item")
}

// Every path key is absolute once ~ is expanded: a relative one, or ~ with HOME unset, is
// refused, as main's internal/config does.
func TestPathsMustBeAbsolute(t *testing.T) {
	dir := t.TempDir()
	global, proj := filepath.Join(dir, "config.toml"), filepath.Join(dir, "projects", "toy.toml")
	write(t, global, "[paths]\nlogbook_dir = \"logs\"\n")
	write(t, proj, "[paths]\nrepo = \"~/r\"\nworktrees = \"wt\"\n")
	_, err := Load(Input{Dir: dir, Project: "toy"})
	wantErr(t, err,
		global+`: paths.logbook_dir = "logs": not an absolute path`,
		proj+`: paths.repo = "~/r": can't expand ~: HOME is unset`,
		proj+`: paths.worktrees = "wt": not an absolute path`)
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
	if k, _, chosen := l.Kind(true); k != CommandKind || !chosen || l.Goal["cl"].Handoff[1] != "cmd:cl_submitted" || !l.Guards.Replace {
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

// A goal's fragment is checked as every path is: ~ expands, but not with HOME unset.
func TestFragmentIsCheckedAsAPath(t *testing.T) {
	for _, c := range []struct{ frag, home, want string }{
		{"~", "/h", ""}, {"/abs/f.md", "", ""}, {"~/f.md", "", "can't expand ~: HOME is unset"}, {"rel.md", "/h", "not an absolute path"},
	} {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "config.toml"), "[goal.g]\nhandoff = [\"clean\"]\nfragment = \""+c.frag+"\"\n")
		_, err := Load(Input{Dir: dir, Home: c.home})
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), "[goal.g] fragment = \""+c.frag+"\": "+c.want)) {
			t.Errorf("fragment %q, HOME %q: %v", c.frag, c.home, err)
		}
	}
}

// An unset table is listed, so bs config shows every key there is.
func TestFieldsListUnsetTables(t *testing.T) {
	l, err := Load(Input{Dir: t.TempDir(), Home: "/h"})
	if err != nil {
		t.Fatal(err)
	}
	var unset []string
	for _, f := range l.Fields() {
		if f.Unset {
			unset = append(unset, f.Key)
		}
	}
	if got := strings.Join(unset, " "); got != "workspace.command goal.<name> checks.<name>" {
		t.Errorf("unset tables = %s", got)
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
	if k, _, chosen := l.Kind(true); k != GitWorktree || chosen {
		t.Error("in a git checkout:", k)
	}
	if k, _, _ := l.Kind(false); k != NoWorkspace {
		t.Error("outside one:", k)
	}
	l.Workspace.Kind = NoWorkspace
	if k, why, chosen := l.Kind(true); k != NoWorkspace || !chosen || !strings.HasPrefix(why, "workspace.kind") {
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

// prune.ignore defaults to one item, a project file overrides the global one whole, an
// empty list clears the default, and a pattern path.Match can't read, or one anchored
// with a leading /, is refused naming its file.
func TestPruneIgnore(t *testing.T) {
	dir := t.TempDir()
	global, proj := filepath.Join(dir, "config.toml"), filepath.Join(dir, "projects", "toy.toml")
	l, err := Load(Input{Dir: dir, Project: "toy", Home: "/h"})
	if err != nil || strings.Join(l.Prune.Ignore, ",") != "bin/" || l.Sources["prune.ignore"].String() != "default" {
		t.Fatalf("default: %q from %s, %v", l.Prune.Ignore, l.Sources["prune.ignore"], err)
	}
	write(t, global, "[prune]\nignore = [\"bin/\", \"*.o\"]\n")
	write(t, proj, "[prune]\nignore = [\"build/\"]\n")
	if l, err = Load(Input{Dir: dir, Project: "toy", Home: "/h"}); err != nil || strings.Join(l.Prune.Ignore, ",") != "build/" ||
		l.Sources["prune.ignore"].String() != "project "+proj {
		t.Fatalf("project: %q from %s, %v", l.Prune.Ignore, l.Sources["prune.ignore"], err)
	}
	write(t, proj, "[prune]\nignore = []\n")
	if l, err = Load(Input{Dir: dir, Project: "toy", Home: "/h"}); err != nil || len(l.Prune.Ignore) != 0 {
		t.Fatalf("cleared: %q, %v", l.Prune.Ignore, err)
	}
	write(t, proj, "[prune]\nignore = [\"[bin\", \"/bin/\", \"a//b\"]\n")
	_, err = Load(Input{Dir: dir, Project: "toy", Home: "/h"})
	wantErr(t, err, proj+`: prune.ignore = "[bin": not a path pattern`, `prune.ignore = "/bin/"`, `prune.ignore = "a//b"`)
}

// agy.bin is agy on PATH or an absolute path to one, ~ expanded; any other base name is
// refused, since herdr's --kind agy starts only a program named agy (bees-14.7).
func TestAgyBin(t *testing.T) {
	for _, c := range []struct{ bin, home, want string }{
		{"agy", "", ""}, {"/opt/x/agy", "", ""}, {"~/bin/agy", "/h", ""},
		{"jetski", "/h", "its base name must be agy"}, {"/opt/x/jetski", "/h", "its base name must be agy"},
		{"bin/agy", "/h", "not an absolute path"}, {"~/agy", "", "can't expand ~: HOME is unset"},
	} {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "config.toml"), "[agy]\nbin = \""+c.bin+"\"\n")
		l, err := Load(Input{Dir: dir, Home: c.home})
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), "agy.bin = \""+c.bin+"\": "+c.want)) {
			t.Errorf("bin %q, HOME %q: %v", c.bin, c.home, err)
		}
		if c.want == "" && l.Agy.Bin != c.bin {
			t.Errorf("bin %q read as %q", c.bin, l.Agy.Bin)
		}
	}
	l, err := Load(Input{Dir: t.TempDir(), Home: "/h"})
	if err != nil || l.Agy.Bin != "agy" || l.Agy.Settings != "~/.gemini/antigravity-cli/settings.json" {
		t.Errorf("defaults: %+v, %v", l.Agy, err)
	}
}
