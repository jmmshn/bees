package main

import (
	"bytes"
	"os"
	"os/exec"
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

// While BS_LOGBOOK_DIR is set, the fixed list of commands skips the config files, so a
// broken file can't silence a fleet; every other command still fails on it (ADR-0008).
func TestSkipListIgnoresTheFilesWhileLogbookDirIsSet(t *testing.T) {
	useTempLogbook(t)
	t.Setenv("BS_LOGBOOK", "")
	t.Setenv("BS_LOGBOOK_DIR", t.TempDir())
	if _, stderr, code := bs(t, "init"); code != 0 {
		t.Fatalf("init: exit %d: %s", code, stderr)
	}
	global := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bees", "config.toml")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("[paths]\nrepoz = \"/x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"send", "--as", "queen-test", "test-1-1", "hi"}, {"inbox", "--as", "test-1-1"}, {"status"}, {"version"}} {
		if _, stderr, code := bs(t, args...); code != 0 {
			t.Errorf("%v: exit %d: %s; want the files skipped", args, code, stderr)
		}
	}
	for _, args := range [][]string{{"init"}, {"config"}, {"spawn", "--as", "queen-test", "--label", "x", "test-1.1"}, {"prune"}} {
		if _, stderr, code := bs(t, args...); code == 0 || !strings.Contains(stderr, global+": unknown key paths.repoz") {
			t.Errorf("%v: exit %d: %s; want the file's error", args, code, stderr)
		}
	}
	t.Setenv("BS_LOGBOOK_DIR", "")
	if _, stderr, code := bs(t, "status"); code == 0 || !strings.Contains(stderr, "unknown key paths.repoz") {
		t.Errorf("status without BS_LOGBOOK_DIR: exit %d: %s; want the file's error", code, stderr)
	}
}

// Moving paths.logbook_dir away from a logbook that exists is refused, by the commands
// that would start a new one too: that would split the store.
func TestMovedLogbookDirIsRefused(t *testing.T) {
	useTempLogbook(t)
	t.Setenv("BS_LOGBOOK", "")
	if _, stderr, code := bs(t, "init"); code != 0 {
		t.Fatalf("init: exit %d: %s", code, stderr)
	}
	old := filepath.Join(os.Getenv("HOME"), ".local/share/bees/test.db")
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("init made no logbook at the default dir: %v", err)
	}
	newDir := t.TempDir()
	global := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bees", "config.toml")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("[paths]\nlogbook_dir = \""+newDir+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"send", "--as", "queen-test", "test-1-1", "hi"}} {
		if _, stderr, code := bs(t, args...); code != 2 || !strings.Contains(stderr, "paths.logbook_dir moved there (global "+global+") from ~/.local/share/bees") {
			t.Errorf("%v: exit %d: %s; want the move refused", args, code, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(newDir, "test.db")); err == nil {
		t.Fatal("a refused command started a logbook at the new dir")
	}
	if err := os.Rename(old, filepath.Join(newDir, "test.db")); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := bs(t, "send", "--as", "queen-test", "test-1-1", "hi"); code != 0 {
		t.Errorf("send after the move: exit %d: %s", code, stderr)
	}
}

// bs config shows the logbook file every command will open and why, and says when the
// skip list is in force.
func TestConfigShowsTheLogbookAndTheSkipList(t *testing.T) {
	configEnv(t)
	t.Setenv("BS_LOGBOOK", "")
	out, stderr, code := bs(t, "config", "--project", "bees")
	if want := "# logbook: " + filepath.Join(os.Getenv("HOME"), ".local/share/bees/bees.db") + " (paths.logbook_dir, default)\n"; code != 0 || !strings.Contains(out, want) {
		t.Errorf("exit %d (%s), output lacks %q:\n%s", code, stderr, want, out)
	}
	if strings.Contains(out, "skip") {
		t.Errorf("output mentions the skip list without BS_LOGBOOK_DIR:\n%s", out)
	}
	t.Setenv("BS_LOGBOOK_DIR", "/e")
	t.Setenv("BS_LOGBOOK", "/x/bees.db")
	out, _, _ = bs(t, "config", "--project", "bees")
	for _, want := range []string{"# logbook: /x/bees.db (env BS_LOGBOOK)\n", "# BS_LOGBOOK_DIR is set, so send, inbox, status, bead, epic and version skip both files\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// bs config names the repo, which link of the chain gave it, and where workers' worktrees go.
func TestConfigShowsTheRepoAndWorktrees(t *testing.T) {
	dir := configEnv(t)
	t.Chdir(t.TempDir())
	if out, _, _ := bs(t, "config", "--project", "bees"); !strings.Contains(out, "# repo: none: bs queen and bs spawn need one; set paths.repo in "+filepath.Join(dir, "projects", "bees.toml")+", or paths.repos\n") {
		t.Errorf("no repo:\n%s", out)
	}
	repos := t.TempDir()
	t.Setenv("BS_REPOS", repos)
	proj := filepath.Join(dir, "projects", "bees.toml")
	if err := os.MkdirAll(filepath.Dir(proj), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proj, []byte("[paths]\nworktrees = \"/trees\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := bs(t, "config", "--project", "bees")
	for _, want := range []string{
		"# repo: " + filepath.Join(repos, "bees") + " (paths.repos, env BS_REPOS)\n",
		"# worker worktrees: /trees/<worker>\n",
		"# cache (launch prompts, worker settings): ",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("exit %d (%s), output lacks %q:\n%s", code, stderr, want, out)
		}
	}
}

// bs config shows the delivery kind and the guards every worker gets: the kind's default
// against the repo's real default branch, or the configured list (ADR-0009).
func TestConfigShowsDeliveryAndTheEffectiveGuards(t *testing.T) {
	dir := configEnv(t)
	t.Chdir(t.TempDir())
	repos := t.TempDir()
	t.Setenv("BS_REPOS", repos)
	repo := filepath.Join(repos, "bees")
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %q: %v: %s", args, err, out)
		}
	}
	out, stderr, code := bs(t, "config", "--project", "bees")
	for _, want := range []string{
		`delivery.kind = "github_pr"  # default`,
		`guards.deny = []  # default`,
		"# guards: a worker is denied (the default for delivery.kind github_pr):\n",
		"#   git push develop*\n",
		"#   gh pr merge*\n",
		"# and its harness may allow, judged by intent:\n",
		"own branch <worker>",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("exit %d (%s), output lacks %q:\n%s", code, stderr, want, out)
		}
	}
	proj := filepath.Join(dir, "projects", "bees.toml")
	if err := os.MkdirAll(filepath.Dir(proj), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proj, []byte("[delivery]\nkind = \"manual\"\n[guards]\ndeny = [\"g4 submit*\"]\nallow_merge_into = [\"dev*\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, _ = bs(t, "config", "--project", "bees")
	for _, want := range []string{
		`delivery.kind = "manual"  # project ` + proj,
		`guards.deny = ["g4 submit*"]  # project ` + proj,
		`# guards: unusable, so bs spawn refuses: guards.allow_merge_into "dev*" matches the default branch develop`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	var parsed map[string]any
	if _, err := toml.Decode(out, &parsed); err != nil {
		t.Errorf("output is not TOML: %v", err)
	}
}
