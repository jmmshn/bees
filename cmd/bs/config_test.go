package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/logbook"
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
		`harness = "claude_code"  # default; global file only`,
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

// bees-12.25: under harness agy, [worker.agy] reaches bs config and the worker's Role.
func TestConfigShowsTheWorkerAgyTable(t *testing.T) {
	dir := configEnv(t)
	global := filepath.Join(dir, "config.toml")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("harness = \"agy\"\n[worker.agy]\nmodel = \"gemini-3.8-flash-low\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"config", "--project", "bees"}, strings.NewReader(""), &out, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if want := `worker.agy.model = "gemini-3.8-flash-low"  # global ` + global; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out.String())
	}
	cfg, err := config.Load("bees")
	if err != nil || cfg.Worker.Role().Agy.Model != "gemini-3.8-flash-low" {
		t.Fatalf("Worker.Role().Agy = %+v, %v; want the [worker.agy] model", cfg.Worker.Role().Agy, err)
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

// bees-12.10: in a recorded herdr pane, the fixed list of commands skips the project file
// and falls back to the defaults when the global file is broken, so a broken file can't
// silence a fleet; every other command, and every command outside the pane, fails on it
// (ADR-0008, ADR-0009 amendment 1).
func TestSkipListIgnoresTheFilesInARecordedPane(t *testing.T) {
	useTempLogbook(t)
	t.Setenv("BS_LOGBOOK", "")
	started(t, "test-1-1", true) // this test process is the pane's recorded worker
	lb, err := logbook.Open(context.Background(), fleetLogbook(t))
	if err != nil {
		t.Fatal(err)
	}
	sp, _, _ := lb.LatestSpawn(context.Background(), "test-1-1")
	lb.Close()
	t.Setenv("HERDR_PANE_ID", sp.Pane)
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bees")
	global, proj := filepath.Join(cfg, "config.toml"), filepath.Join(cfg, "projects", "test.toml")
	for _, f := range []string{global, proj} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("[paths]\nrepoz = \"/x\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"send", "queen-test", "hi"}, {"inbox"}, {"status"}, {"version"}} {
		if _, stderr, code := bs(t, args...); code != 0 {
			t.Errorf("%v: exit %d: %s; want the files skipped", args, code, stderr)
		}
	}
	for _, args := range [][]string{{"init"}, {"config"}, {"spawn", "--label", "x", "test-1.2"}, {"prune"}} {
		if _, stderr, code := bs(t, args...); code == 0 || !strings.Contains(stderr, ": unknown key paths.repoz") {
			t.Errorf("%v: exit %d: %s; want the file's error", args, code, stderr)
		}
	}
	t.Setenv("HERDR_PANE_ID", "")
	if _, stderr, code := bs(t, "status"); code == 0 || !strings.Contains(stderr, "unknown key paths.repoz") {
		t.Errorf("status outside the pane: exit %d: %s; want the file's error", code, stderr)
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
	for _, want := range []string{"# logbook: /x/bees.db (env BS_LOGBOOK)\n", "# BS_LOGBOOK_DIR is set: it beats paths.logbook_dir in this shell, never in a herdr pane bs recorded"} {
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
	// Kind none: bs config says where workers open, or that none start (bees-12.28).
	client := t.TempDir()
	for body, want := range map[string]string{
		"[workspace]\nkind = \"none\"\n":                                     "# workers: none start: workspace.kind = none opens them on paths.repo, which is unset",
		"[workspace]\nkind = \"none\"\n[paths]\nrepo = \"" + client + "\"\n": "# workers: share " + client + ", each in its own herdr workspace, no worktree or branch",
	} {
		if err := os.WriteFile(proj, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, stderr, code := bs(t, "config", "--project", "bees"); code != 0 || !strings.Contains(out, want) {
			t.Errorf("%s: exit %d (%s), output lacks %q:\n%s", body, code, stderr, want, out)
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
	// The keys come before the long deny list, so it doesn't bury them (bees-12.44).
	if k, g := strings.Index(out, "\ndelivery.kind = "), strings.Index(out, "\n# guards: "); k < 0 || g < k {
		t.Errorf("keys at %d, guards at %d: want the keys first:\n%s", k, g, out)
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
		`# guards: unusable, so bs queen and bs spawn refuse: guards.allow_merge_into: delivery.kind manual has no pull requests to merge`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	var parsed map[string]any
	if _, err := toml.Decode(out, &parsed); err != nil {
		t.Errorf("output is not TOML: %v", err)
	}
	// Under github_pr a merge grant holds only where GitHub protects the default branch.
	useFakeHive(t) // no GitHub
	if err := os.WriteFile(proj, []byte("[guards]\nallow_merge_into = [\"pilot/*\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, _ = bs(t, "config", "--project", "bees")
	if want := "# guards: unusable, so bs queen and bs spawn refuse: guards.allow_merge_into: GitHub doesn't confirm the repo's default branch is protected"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}

// bees-12.9: bs config init writes a commented global file that loads clean, shows every
// key with its default, and sets only what its flags name.
func TestConfigInitWritesCommentedGlobalFile(t *testing.T) {
	dir := configEnv(t)
	var out, stderr bytes.Buffer
	if code := run([]string{"config", "init", "--workspace", "none", "--delivery", "manual"}, strings.NewReader(""), &out, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	path := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{
		"# harness = \"claude_code\"\n",
		"[workspace]\n",
		"kind = \"none\"\n",
		"[delivery]\n",
		"kind = \"manual\"\n",
		"# logbook_dir = \"~/.local/share/bees\"\n",
		"# deny = []\n",
		"[worker.claude_code]\n",
		"# model = \"\"\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("config.toml lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "repo = ") {
		t.Errorf("the global file shows paths.repo, a project-only key:\n%s", text)
	}
	l, err := config.Load("")
	if err != nil {
		t.Fatalf("written file doesn't load: %v", err)
	}
	if l.Workspace.Kind != "none" || l.Delivery.Kind != "manual" || l.Harness != "claude_code" {
		t.Errorf("loaded %+v %+v %q", l.Workspace, l.Delivery, l.Harness)
	}
	if !strings.Contains(out.String(), path) {
		t.Errorf("stdout doesn't name the file: %q", out.String())
	}
	// A second init never overwrites the keeper's file.
	stderr.Reset()
	if code := run([]string{"config", "init"}, strings.NewReader(""), &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "already exists") {
		t.Errorf("second init: exit %d, %q", code, stderr.String())
	}
}

// A project file holds only project-scope keys, takes --repo, and refuses --harness.
func TestConfigInitProjectFile(t *testing.T) {
	dir := configEnv(t)
	repo := t.TempDir()
	var out, stderr bytes.Buffer
	args := []string{"config", "init", "--project", "toy", "--workspace", "none", "--delivery", "manual", "--repo", repo}
	if code := run(args, strings.NewReader(""), &out, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, "projects", "toy.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "repo = "+config.Quote(repo)+"\n") || strings.Contains(string(b), "harness = ") || strings.Contains(string(b), "logbook_dir = ") {
		t.Errorf("project file:\n%s", b)
	}
	l, err := config.Load("toy")
	if err != nil {
		t.Fatal(err)
	}
	if l.Paths.Repo != repo || l.Workspace.Kind != "none" {
		t.Errorf("loaded %+v %+v", l.Paths, l.Workspace)
	}
	for _, bad := range [][]string{
		{"config", "init", "--project", "other", "--harness", "claude_code"},
		{"config", "init", "--repo", repo},
		{"config", "init", "--workspace", "citc"},
		{"config", "init", "--project", "other", "--repo", "relative/dir"},
	} {
		stderr.Reset()
		if code := run(bad, strings.NewReader(""), &out, &stderr); code != 2 {
			t.Errorf("%v: exit %d, want 2: %s", bad, code, stderr.String())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); err == nil {
		t.Error("a refused init wrote the global file")
	}
}

// bs config init refuses what bs queen and bs spawn would: none under github_pr, none with
// no directory, a repo or fragment that isn't there.
func TestConfigInitRefusesWhatSpawnWould(t *testing.T) {
	dir := configEnv(t)
	missing := filepath.Join(t.TempDir(), "nope")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--workspace", "none"}, `delivery.kind = "github_pr"`},
		{[]string{"--project", "toy", "--workspace", "none", "--delivery", "manual"}, "pass --repo <dir>"},
		{[]string{"--project", "toy", "--repo", missing}, "no such directory"},
		{[]string{"--fragment", missing}, "delivery.fragment"},
	} {
		var out, stderr bytes.Buffer
		if code := run(append([]string{"config", "init"}, c.args...), strings.NewReader(""), &out, &stderr); code != 2 || !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%v: exit %d, want 2 with %q: %s", c.args, code, c.want, stderr.String())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) > 1 { // projects/ may exist; no file in it
		t.Errorf("a refused init left files: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); err == nil {
		t.Error("a refused init wrote the global file")
	}
}
