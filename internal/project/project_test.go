package project_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/proc"
	"github.com/jmmshn/bees/internal/project"
)

// gitRepo creates a git repo named name under a temp dir and returns its path.
func gitRepo(t *testing.T, name string) string {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir()) // macOS: /var is /private/var, as git reports it
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, name)
	git(t, "", "init", "-q", "-b", "main", dir)
	git(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// resolve is Name then Locate, with the logbook under /lb and repos from BS_REPOS: what bs
// does with no config file.
func resolve(flag string) (project.Project, error) {
	name, err := project.Name(flag)
	if err != nil {
		return project.Project{}, err
	}
	return project.Locate(name, project.Paths{LogbookDir: "/lb", Repos: os.Getenv("BS_REPOS")})
}

func clearEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"BS_PROJECT", "BS_SESSION", "BS_LOGBOOK", "BS_REPOS"} {
		t.Setenv(k, "")
	}
}

func TestProjectComesFromGitRepo(t *testing.T) {
	clearEnv(t)
	repo := gitRepo(t, "bees")
	t.Chdir(repo)
	p, err := resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "bees" || p.Repo != repo {
		t.Fatalf("got %+v, want name bees, repo %s", p, repo)
	}
	if p.Queen() != "queen-bees" {
		t.Fatalf("queen = %s", p.Queen())
	}
}

func TestLinkedWorktreeResolvesToMainRepo(t *testing.T) {
	clearEnv(t)
	repo := gitRepo(t, "bees")
	wt := filepath.Join(filepath.Dir(repo), "bees-WT", "beads-995-8")
	git(t, repo, "worktree", "add", "-q", "-b", "beads-995-8", wt)
	t.Chdir(wt)
	p, err := resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "bees" || p.Repo != repo {
		t.Fatalf("got %+v, want the main repo %s", p, repo)
	}
}

func TestEnvBeatsGitAndFlagBeatsEnv(t *testing.T) {
	clearEnv(t)
	repos := t.TempDir()
	t.Setenv("BS_REPOS", repos)
	t.Chdir(gitRepo(t, "bees"))
	t.Setenv("BS_PROJECT", "dotfiles")
	p, err := resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "dotfiles" || p.Repo != filepath.Join(repos, "dotfiles") {
		t.Fatalf("env: got %+v", p)
	}
	if p, _ = resolve("crew"); p.Name != "crew" {
		t.Fatalf("flag: got %+v", p)
	}
}

func TestLogbookOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv("BS_REPOS", t.TempDir())
	t.Setenv("BS_LOGBOOK", "/tmp/x.db")
	p, err := resolve("bees")
	if err != nil {
		t.Fatal(err)
	}
	if p.Logbook != "/tmp/x.db" {
		t.Fatalf("logbook = %s", p.Logbook)
	}
	t.Setenv("BS_LOGBOOK", "")
	if p, _ = resolve("bees"); p.Logbook != "/lb/bees.db" {
		t.Fatalf("logbook = %s, want it under paths.logbook_dir", p.Logbook)
	}
}

func TestNoProjectOutsideGit(t *testing.T) {
	clearEnv(t)
	t.Chdir(t.TempDir())
	if _, err := resolve(""); !errors.Is(err, project.ErrNoProject) {
		t.Fatalf("err = %v, want ErrNoProject", err)
	}
}

func TestNamedProjectNeedsReposOutsideItsGitRepo(t *testing.T) {
	clearEnv(t)
	t.Chdir(t.TempDir())
	p, err := resolve("bees")
	if err != nil || p.Repo != "" {
		t.Fatalf("outside git: got %+v, %v; want no repo and no error (messages need only the logbook)", p, err)
	}
	if err := p.NeedRepo(); !errors.Is(err, project.ErrNoRepos) {
		t.Fatalf("outside git: NeedRepo = %v, want ErrNoRepos", err)
	}
	t.Chdir(gitRepo(t, "dotfiles"))
	if p, _ := resolve("bees"); !errors.Is(p.NeedRepo(), project.ErrNoRepos) {
		t.Fatalf("in another project's repo: got %+v, want ErrNoRepos", p)
	}
}

func TestNamedProjectUsesItsOwnGitRepo(t *testing.T) {
	clearEnv(t)
	repo := gitRepo(t, "jobflow-ray")
	t.Chdir(repo)
	t.Setenv("BS_PROJECT", "jobflowray") // as in every worker's settings
	p, err := resolve("")
	if err != nil || p.Repo != repo {
		t.Fatalf("got %+v, %v; want repo %s with no BS_REPOS", p, err, repo)
	}
}

// register makes the test binary's parent a Claude session called name, launched in cwd.
func register(t *testing.T, name, cwd string) {
	t.Helper()
	reg := filepath.Join(os.Getenv("HOME"), ".claude", "sessions")
	if err := os.MkdirAll(reg, 0o755); err != nil {
		t.Fatal(err)
	}
	start, err := proc.Start(os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"name": name, "pid": os.Getppid(), "procStart": start, "cwd": cwd})
	if err := os.WriteFile(filepath.Join(reg, fmt.Sprintf("%d.json", os.Getppid())), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Outside a recorded pane, an inherited BS_PROJECT beats the git repo, wherever bs runs:
// a session bs started finds its project from its pane instead (ADR-0009 amendment 1).
func TestBSProjectBeatsTheRepo(t *testing.T) {
	clearEnv(t)
	t.Chdir(gitRepo(t, "bsdemo"))
	t.Setenv("BS_PROJECT", "bees")
	if p, err := resolve(""); err != nil || p.Name != "bees" {
		t.Fatalf("%+v, %v; want bees", p, err)
	}
	if p, err := resolve("bsdemo"); err != nil || p.Name != "bsdemo" {
		t.Fatalf("--project bsdemo: %+v, %v", p, err)
	}
}

func TestMeResolutionOrder(t *testing.T) {
	clearEnv(t)
	if _, err := project.Me(""); !errors.Is(err, project.ErrNoSession) {
		t.Fatalf("nothing set: err = %v, want ErrNoSession", err)
	}
	t.Setenv("BS_SESSION", "outside-claude")
	if me, _ := project.Me(""); me != "outside-claude" {
		t.Fatalf("env outside Claude: %q", me)
	}
	reg := filepath.Join(os.Getenv("HOME"), ".claude", "sessions")
	if err := os.MkdirAll(reg, 0o755); err != nil {
		t.Fatal(err)
	}
	// The test binary's parent stands in for the Claude process that ran bs.
	start, err := proc.Start(os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(reg, fmt.Sprintf("%d.json", os.Getppid()))
	writeEntry := func(procStart string) {
		body := fmt.Sprintf(`{"name":"beads-995-8","pid":%d,"procStart":%q}`, os.Getppid(), procStart)
		if err := os.WriteFile(entry, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A stale entry on a reused pid names no one (bees-8.2 review).
	writeEntry("Mon Jan  1 00:00:00 2001")
	if me, err := project.Me(""); err != nil || me != "outside-claude" {
		t.Fatalf("stale entry: %q, %v; want the env name", me, err)
	}
	writeEntry(start)
	if me, err := project.Me(""); err != nil || me != "beads-995-8" {
		t.Fatalf("ppid walk: %q, %v", me, err)
	}
	// bs usually runs from a shell inside Claude: the walk must climb past an unregistered
	// parent to the grandparent, which needs a real process lookup on every OS (bees-8.2).
	gp, err := proc.Parent(os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	gpStart, err := proc.Start(gp)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(entry)
	body := fmt.Sprintf(`{"name":"beads-995-8","pid":%d,"procStart":%q}`, gp, gpStart)
	if err := os.WriteFile(filepath.Join(reg, fmt.Sprintf("%d.json", gp)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if me, err := project.Me(""); err != nil || me != "beads-995-8" {
		t.Fatalf("grandparent walk: %q, %v", me, err)
	}
	t.Setenv("BS_SESSION", "queen-bees") // a stale or inherited value must not beat the registry
	if me, _ := project.Me(""); me != "beads-995-8" {
		t.Fatalf("registry vs env: %q", me)
	}
	// Inside a Claude session the registry is who you are: --as may only repeat it (bees-7.4).
	if me, err := project.Me("beads-995-8"); err != nil || me != "beads-995-8" {
		t.Fatalf("flag naming this session: %q, %v", me, err)
	}
	if me, err := project.Me("queen-bees"); !errors.Is(err, project.ErrNotYou) {
		t.Fatalf("flag naming another session: %q, %v; want ErrNotYou", me, err)
	}
	clearEnv(t) // outside Claude, the flag names you
	if me, _ := project.Me("keeper"); me != "keeper" {
		t.Fatalf("flag: %q", me)
	}
}

func TestProjectNamesAreNormalisedAndValidated(t *testing.T) {
	clearEnv(t)
	for raw, want := range map[string]string{
		"bees": "bees", "pymatgen-analysis-defects": "pymatgenanalysisdefects",
		"web-2": "web2", "Jobflow-Ray": "jobflowray", "obsidian_vault": "obsidianvault",
	} {
		p, err := resolve(raw)
		if err != nil || p.Name != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", raw, p.Name, err, want)
		}
	}
	for _, bad := range []string{"2fa-app", "my.repo", "has space", "a-very-long-project-name-that-overflows"} {
		if _, err := resolve(bad); !errors.Is(err, project.ErrBadProject) {
			t.Errorf("Resolve(%q): err = %v, want ErrBadProject", bad, err)
		}
	}
}

func TestGitDirNameIsNormalisedButRepoPathIsNot(t *testing.T) {
	clearEnv(t)
	repo := gitRepo(t, "jobflow-ray")
	t.Chdir(repo)
	p, err := resolve("")
	if err != nil || p.Name != "jobflowray" || p.Repo != repo {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestRepoPathFromNameFindsTheDirWhoseNormalisedNameMatches(t *testing.T) {
	clearEnv(t)
	repos := t.TempDir()
	t.Setenv("BS_REPOS", repos)
	want := filepath.Join(repos, "pymatgen-analysis-defects")
	if err := os.Mkdir(want, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pymatgen-analysis-defects", "pymatgenanalysisdefects", "Pymatgen_Analysis_Defects"} {
		if p, err := resolve(name); err != nil || p.Repo != want {
			t.Errorf("Resolve(%q) = %+v, %v; want Repo %s", name, p, err, want)
		}
	}
}

// The repo chain (ADR-0008): --repo > the git repo you are in, when it is the project's >
// paths.repo > paths.repos. Each project.Project says which link won.
func TestRepoChain(t *testing.T) {
	clearEnv(t)
	own, other, configured := gitRepo(t, "bees"), gitRepo(t, "dotfiles"), gitRepo(t, "bees")
	repos := t.TempDir()
	locate := func(c project.Paths) project.Project {
		t.Helper()
		c.LogbookDir, c.Worktrees = "/lb", "{repo}-WT"
		p, err := project.Locate("bees", c)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Chdir(own)
	for _, c := range []struct {
		paths      project.Paths
		repo, from string
	}{
		{project.Paths{RepoFlag: other, Repo: configured, Repos: repos}, other, "--repo"},
		{project.Paths{Repo: configured, Repos: repos}, own, "git"},
		// Kind none opens workers on paths.repo, so it beats the checkout bs runs in.
		{project.Paths{Repo: configured, Repos: repos, AnyVCS: true}, configured, "paths.repo"},
		{project.Paths{RepoFlag: repos, Repo: configured, AnyVCS: true}, repos, "--repo"}, // a plain dir, as given
	} {
		if p := locate(c.paths); p.Repo != c.repo || p.RepoFrom != c.from || p.Worktrees != c.repo+"-WT" {
			t.Errorf("%+v: got %+v, want repo %s from %s", c.paths, p, c.repo, c.from)
		}
	}
	t.Chdir(t.TempDir())
	if p := locate(project.Paths{Repo: configured, Repos: repos}); p.Repo != configured || p.RepoFrom != "paths.repo" {
		t.Errorf("outside git: got %+v, want paths.repo %s", p, configured)
	}
	if p := locate(project.Paths{Repos: repos}); p.Repo != filepath.Join(repos, "bees") || p.RepoFrom != "paths.repos" {
		t.Errorf("only repos: got %+v", p)
	}
	if p := locate(project.Paths{}); p.Repo != "" || p.RepoFrom != "" || p.Worktrees != "" {
		t.Errorf("nothing: got %+v, want no repo", p)
	}
	// A non-repo fails only what needs a repo: messages need only the logbook.
	for _, c := range []project.Paths{{RepoFlag: repos}, {Repo: repos}} {
		c.LogbookDir = "/lb"
		p, err := project.Locate("bees", c)
		if err != nil || p.NeedRepo() == nil || !strings.Contains(p.NeedRepo().Error(), "not a git checkout") {
			t.Errorf("%+v: Locate err = %v, NeedRepo = %v; want only NeedRepo to refuse a non-repo", c, err, p.NeedRepo())
		}
	}
	// Under workspace kind none, paths.repo may be any directory: a jj or hg checkout.
	if p, err := project.Locate("bees", project.Paths{LogbookDir: "/lb", Repo: repos, AnyVCS: true}); err != nil || p.NeedRepo() != nil || p.Repo != repos || p.Base != "" {
		t.Errorf("kind none, plain dir: got %+v, %v, NeedRepo = %v", p, err, p.NeedRepo())
	}
	if p, _ := project.Locate("bees", project.Paths{LogbookDir: "/lb", Repo: filepath.Join(repos, "nope"), AnyVCS: true}); p.NeedRepo() == nil || !strings.Contains(p.NeedRepo().Error(), "not a directory") {
		t.Errorf("kind none, missing dir: NeedRepo = %v, want it refused", p.NeedRepo())
	}
}

// A repo from --repo or paths.repo that is a linked worktree is the base workers branch
// from, never where their worktrees live: that stays under the main checkout's -WT.
func TestAWorktreeRepoIsTheBaseNotTheLocation(t *testing.T) {
	clearEnv(t)
	main := gitRepo(t, "bees")
	wt := filepath.Join(filepath.Dir(main), "bees-WT", "bees-1-2")
	git(t, main, "worktree", "add", "-q", "-b", "bees-1-2", wt)
	t.Chdir(t.TempDir())
	for _, c := range []project.Paths{{RepoFlag: wt}, {RepoFlag: filepath.Join(wt, ".")}, {Repo: wt}} {
		c.LogbookDir, c.Worktrees = "/lb", "{repo}-WT"
		p, err := project.Locate("bees", c)
		if err != nil || p.Repo != main || p.Base != wt || p.Worktrees != main+"-WT" {
			t.Errorf("%+v: got %+v, %v; want repo %s, base %s", c, p, err, main, wt)
		}
	}
	p, _ := project.Locate("bees", project.Paths{LogbookDir: "/lb", Repo: main, Worktrees: "/trees"})
	if p.Base != "" || p.BaseRepo() != main || p.Worktrees != "/trees" {
		t.Errorf("main checkout: got %+v, want no separate base", p)
	}
}
