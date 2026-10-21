package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// setup points XDG_CONFIG_HOME at a temp dir, clears the env overrides and returns the
// config dir.
func setup(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	for _, k := range []string{"BS_LOGBOOK_DIR", "BS_REPOS"} {
		t.Setenv(k, "")
	}
	return filepath.Join(xdg, "bees")
}

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

func TestNoFilesGiveDefaults(t *testing.T) {
	dir := setup(t)
	l, err := Load("bees")
	if err != nil {
		t.Fatal(err)
	}
	if l.Paths.LogbookDir != "~/.local/share/bees" || l.Paths.Worktrees != "{repo}-WT" || l.Worker.ClaudeCode.Model != "" {
		t.Errorf("defaults = %+v", l.Config)
	}
	for _, f := range l.Fields() {
		if s := l.Sources[f.Key]; s.String() != "default" {
			t.Errorf("%s from %s, want default", f.Key, s)
		}
	}
	want := []File{
		{Scope: "global", Path: filepath.Join(dir, "config.toml")},
		{Scope: "project", Path: filepath.Join(dir, "projects", "bees.toml")},
	}
	if !reflect.DeepEqual(l.Files, want) {
		t.Errorf("files = %+v, want %+v", l.Files, want)
	}
}

func TestPrecedenceDefaultGlobalProjectEnv(t *testing.T) {
	dir := setup(t)
	global, proj := filepath.Join(dir, "config.toml"), filepath.Join(dir, "projects", "bees.toml")
	write(t, global, "[paths]\nlogbook_dir = \"/g/logs\"\nrepos = \"/g/repos\"\n[worker.claude_code]\nmodel = \"opus\"\n")
	write(t, proj, "[paths]\nlogbook_dir = \"/p/logs\"\nrepo = \"/p/bees\"\n[worker.claude_code]\nmodel = \"sonnet\"\n")
	t.Setenv("BS_REPOS", "/e/repos")
	l, err := Load("bees")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]struct{ val, src string }{
		"paths.logbook_dir":        {"/p/logs", "project " + proj},
		"paths.repos":              {"/e/repos", "env BS_REPOS"},
		"paths.repo":               {"/p/bees", "project " + proj},
		"paths.worktrees":          {"{repo}-WT", "default"},
		"worker.claude_code.model": {"sonnet", "project " + proj},
		"queen.claude_code.model":  {"", "default"},
	} {
		i := slices.IndexFunc(l.Fields(), func(f Field) bool { return f.Key == key })
		if i < 0 {
			t.Errorf("no setting %s", key)
			continue
		}
		if got := l.Fields()[i].Value; got != want.val || l.Sources[key].String() != want.src {
			t.Errorf("%s = %q (%s), want %q (%s)", key, got, l.Sources[key], want.val, want.src)
		}
	}
	if !l.Files[0].Exists || !l.Files[1].Exists {
		t.Errorf("files = %+v, want both existing", l.Files)
	}
}

func TestNoProjectReadsGlobalOnly(t *testing.T) {
	dir := setup(t)
	write(t, filepath.Join(dir, "projects", "bees.toml"), "[paths]\nrepo = \"/p\"\n")
	l, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if l.Paths.Repo != "" || len(l.Files) != 1 {
		t.Errorf("repo = %q, files = %+v; want no project file read", l.Paths.Repo, l.Files)
	}
}

func TestUnknownKeyNamesFileAndKeyPath(t *testing.T) {
	dir := setup(t)
	global := filepath.Join(dir, "config.toml")
	write(t, global, "[worker.claude_code]\nmdoel = \"opus\"\n[worker.oh_my_pi]\nmodel = \"x\"\n")
	_, err := Load("bees")
	wantErr(t, err, global+": unknown key worker.claude_code.mdoel", global+": unknown key worker.oh_my_pi")
	if strings.Contains(err.Error(), "oh_my_pi.model") {
		t.Errorf("error %q names each key of an unknown table, not just the table", err)
	}
}

func TestWrongScopeKey(t *testing.T) {
	dir := setup(t)
	global, proj := filepath.Join(dir, "config.toml"), filepath.Join(dir, "projects", "bees.toml")
	write(t, global, "[paths]\nrepo = \"/r\"\n")
	write(t, proj, "[paths]\nrepos = \"/rs\"\n")
	_, err := Load("bees")
	wantErr(t, err,
		global+": paths.repo belongs in the project file, not the global one",
		proj+": paths.repos belongs in the global file, not the project one")
}

func TestMisnamedProjectFileNamesTheRightOne(t *testing.T) {
	dir := setup(t)
	write(t, filepath.Join(dir, "projects", "jobflow-ray.toml"), "")
	write(t, filepath.Join(dir, "projects", "other.toml"), "") // another project's: not ours to judge
	_, err := Load("jobflowray")
	wantErr(t, err, "jobflow-ray.toml: bs never reads it; rename it to "+filepath.Join(dir, "projects", "jobflowray.toml"))
	if strings.Contains(err.Error(), "other.toml") {
		t.Errorf("error %q blames another project's file", err)
	}
}

func TestSyntaxErrorNamesFileAndLine(t *testing.T) {
	dir := setup(t)
	global := filepath.Join(dir, "config.toml")
	write(t, global, "[paths]\nrepos = \n")
	_, err := Load("")
	wantErr(t, err, global+": ", "line 2")
}

func TestWrongTypeNamesFileAndKey(t *testing.T) {
	dir := setup(t)
	global := filepath.Join(dir, "config.toml")
	write(t, global, "[paths]\nrepos = 3\n")
	_, err := Load("")
	wantErr(t, err, global+": ", "paths.repos")
}

func TestUnreadableFileIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	dir := setup(t)
	global := filepath.Join(dir, "config.toml")
	write(t, global, "")
	if err := os.Chmod(global, 0); err != nil {
		t.Fatal(err)
	}
	_, err := Load("")
	wantErr(t, err, global)
}

func TestDirFallsBackToHomeAndRefusesRelativeXDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	if d, err := Dir(); err != nil || d != filepath.Join(home, ".config", "bees") {
		t.Errorf("Dir() = %q, %v", d, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "rel")
	if _, err := Load(""); err == nil {
		t.Error("a relative XDG_CONFIG_HOME loaded")
	}
}

// Every setting declares its key and a known scope: a typo'd tag would make a key
// nobody can set.
func TestEverySettingIsTagged(t *testing.T) {
	var c Config
	for _, f := range leaves(reflect.ValueOf(&c).Elem(), "") {
		switch f.tag.Get("scope") {
		case "global", "project", "both":
		default:
			t.Errorf("%s: scope %q", f.key, f.tag.Get("scope"))
		}
		if strings.Contains(f.key, "..") || strings.HasPrefix(f.key, ".") || strings.HasSuffix(f.key, ".") {
			t.Errorf("%s: a field has no toml tag", f.key)
		}
	}
}

// enumConfig exercises the enum tag, which no v1 setting uses yet.
type enumConfig struct {
	Worker struct {
		Mode string `toml:"mode" scope:"both" env:"BS_TEST_MODE" default:"auto" enum:"auto|accept_edits"`
	} `toml:"worker"`
}

func TestEnumIsChecked(t *testing.T) {
	dir := setup(t)
	global := filepath.Join(dir, "config.toml")
	write(t, global, "[worker]\nmode = \"yolo\"\n")
	var c enumConfig
	err := load(&c, map[string]Source{}, map[string][]string{}, &[]File{}, dir, "")
	wantErr(t, err, global+`: worker.mode = "yolo": must be one of auto, accept_edits`)

	write(t, global, "[worker]\nmode = \"accept_edits\"\n")
	t.Setenv("BS_TEST_MODE", "nope")
	err = load(&c, map[string]Source{}, map[string][]string{}, &[]File{}, dir, "")
	wantErr(t, err, `env BS_TEST_MODE: worker.mode = "nope"`)
}

func TestPathsExpandTildeOnlyAndMustBeAbsolute(t *testing.T) {
	dir := setup(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(dir, "config.toml")
	write(t, global, "[paths]\nlogbook_dir = \"~/logs\"\nrepos = \"~\"\nworktrees = \"{repo}-trees\"\n")
	l, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for got, want := range map[string]string{Expand(l.Paths.LogbookDir): home + "/logs", Expand(l.Paths.Repos): home} {
		if got != want {
			t.Errorf("expanded %q, want %q", got, want)
		}
	}
	for body, why := range map[string]string{
		`logbook_dir = "logs"`:       `paths.logbook_dir = "logs": not an absolute path`,
		`logbook_dir = "$HOME/logs"`: `paths.logbook_dir = "$HOME/logs": not an absolute path`,
		`logbook_dir = "~bob/logs"`:  `paths.logbook_dir = "~bob/logs": not an absolute path`,
		`logbook_dir = ""`:           `paths.logbook_dir = "": not an absolute path`,
		`logbook_dir = "/x/{repo}"`:  `paths.logbook_dir = "/x/{repo}": {repo} is only allowed at the start of a worktrees path`,
		`worktrees = "/wt/{repo}"`:   `paths.worktrees = "/wt/{repo}": {repo} is only allowed at its start`,
		`worktrees = "wt"`:           `paths.worktrees = "wt": not an absolute path`,
	} {
		write(t, global, "[paths]\n"+body+"\n")
		_, err := Load("")
		wantErr(t, err, global+": "+why)
	}
	write(t, global, "")
	t.Setenv("BS_REPOS", "rel/repos")
	_, err = Load("")
	wantErr(t, err, `env BS_REPOS: paths.repos = "rel/repos": not an absolute path`)
}

// Shadowed lists the values a setting held before the one that won, so bs can tell a moved
// logbook_dir from a new one.
func TestShadowedListsTheLowerValues(t *testing.T) {
	dir := setup(t)
	write(t, filepath.Join(dir, "config.toml"), "[paths]\nlogbook_dir = \"/g\"\n")
	write(t, filepath.Join(dir, "projects", "bees.toml"), "[paths]\nlogbook_dir = \"/p\"\n")
	t.Setenv("BS_LOGBOOK_DIR", "/e")
	l, err := Load("bees")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := l.Shadowed["paths.logbook_dir"], []string{"~/.local/share/bees", "/g", "/p"}; !slices.Equal(got, want) {
		t.Errorf("shadowed = %q, want %q", got, want)
	}
}

// Env skips both files: the fixed list of commands reads only the environment once
// BS_LOGBOOK_DIR is set, so a broken file can't silence a running fleet.
func TestEnvSkipsTheFiles(t *testing.T) {
	dir := setup(t)
	write(t, filepath.Join(dir, "config.toml"), "broken = [\n")
	t.Setenv("BS_LOGBOOK_DIR", "/e")
	l, err := Env()
	if err != nil || l.Paths.LogbookDir != "/e" || len(l.Files) != 0 {
		t.Fatalf("Env() = %+v, %v", l, err)
	}
}

// bees-12.6: harness is a global-only enum, default claude_code.
func TestHarnessIsAGlobalEnum(t *testing.T) {
	dir := setup(t)
	global, proj := filepath.Join(dir, "config.toml"), filepath.Join(dir, "projects", "bees.toml")
	if l, err := Load("bees"); err != nil || l.Harness != "claude_code" {
		t.Fatalf("default harness = %v, %v; want claude_code", l, err)
	}
	write(t, global, "harness = \"agy\"\n[worker.claude_code]\nmodel = \"m\"\n")
	write(t, proj, "harness = \"claude_code\"\n")
	_, err := Load("bees")
	wantErr(t, err, global+`: harness = "agy": must be one of claude_code`, proj+": harness belongs in the global file, not the project one")
	if strings.Contains(err.Error(), "is for harness") {
		t.Errorf("error %q also calls every table inactive under a harness that isn't one", err)
	}
}

// bees-12.6: a table for a harness other than the active one is an error naming the file,
// the table and the harness key. Only claude_code exists, so the active one is forced.
func TestAnInactiveHarnessTableIsAnError(t *testing.T) {
	dir := setup(t)
	proj := filepath.Join(dir, "projects", "bees.toml")
	write(t, proj, "[worker.claude_code]\nmodel = \"sonnet\"\n[queen.claude_code]\nmodel = \"opus\"\n")
	l, err := Load("bees")
	if err != nil {
		t.Fatal(err)
	}
	errs := inactive(reflect.ValueOf(&l.Config).Elem(), l.Sources, "oh_my_pi")
	if len(errs) != 2 {
		t.Fatalf("errs = %v, want one per table", errs)
	}
	wantErr(t, errs[1], proj+": [worker.claude_code] is for harness claude_code, but the harness key says oh_my_pi")
}
