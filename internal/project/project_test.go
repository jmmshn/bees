package project_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jmmshn/bees/internal/project"
)

// gitRepo creates a git repo named name under a temp dir and returns its path.
func gitRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
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
	p, err := project.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "bees" || p.Repo != repo {
		t.Fatalf("got %+v, want name bees, repo %s", p, repo)
	}
	if want := filepath.Join(os.Getenv("HOME"), ".local/share/bees/bees.db"); p.Logbook != want {
		t.Fatalf("logbook = %s, want %s", p.Logbook, want)
	}
	if p.Queen() != "queen-bees" {
		t.Fatalf("queen = %s", p.Queen())
	}
}

func TestLinkedWorktreeResolvesToMainRepo(t *testing.T) {
	clearEnv(t)
	repo := gitRepo(t, "bees")
	wt := filepath.Join(filepath.Dir(repo), "bees-WT", "bee-995-8")
	git(t, repo, "worktree", "add", "-q", "-b", "bee-995-8", wt)
	t.Chdir(wt)
	p, err := project.Resolve("")
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
	p, err := project.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "dotfiles" || p.Repo != filepath.Join(repos, "dotfiles") {
		t.Fatalf("env: got %+v", p)
	}
	if p, _ = project.Resolve("crew"); p.Name != "crew" {
		t.Fatalf("flag: got %+v", p)
	}
}

func TestLogbookOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv("BS_LOGBOOK", "/tmp/x.db")
	p, err := project.Resolve("bees")
	if err != nil {
		t.Fatal(err)
	}
	if p.Logbook != "/tmp/x.db" {
		t.Fatalf("logbook = %s", p.Logbook)
	}
}

func TestNoProjectOutsideGit(t *testing.T) {
	clearEnv(t)
	t.Chdir(t.TempDir())
	if _, err := project.Resolve(""); !errors.Is(err, project.ErrNoProject) {
		t.Fatalf("err = %v, want ErrNoProject", err)
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
	body := `{"name":"bee-995-8","pid":` + fmt.Sprint(os.Getppid()) + `}`
	if err := os.WriteFile(filepath.Join(reg, fmt.Sprintf("%d.json", os.Getppid())), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if me, err := project.Me(""); err != nil || me != "bee-995-8" {
		t.Fatalf("ppid walk: %q, %v", me, err)
	}
	t.Setenv("BS_SESSION", "queen-bees") // a stale or inherited value must not beat the registry
	if me, _ := project.Me(""); me != "bee-995-8" {
		t.Fatalf("registry vs env: %q", me)
	}
	if me, _ := project.Me("captain"); me != "captain" {
		t.Fatalf("flag: %q", me)
	}
}

func TestProjectNamesAreNormalisedAndValidated(t *testing.T) {
	clearEnv(t)
	for raw, want := range map[string]string{
		"bees": "bees", "pymatgen-analysis-defects": "pymatgenanalysisdefects",
		"web-2": "web2", "Jobflow-Ray": "jobflowray", "obsidian_vault": "obsidianvault",
	} {
		p, err := project.Resolve(raw)
		if err != nil || p.Name != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", raw, p.Name, err, want)
		}
	}
	for _, bad := range []string{"2fa-app", "my.repo", "has space", "a-very-long-project-name-that-overflows"} {
		if _, err := project.Resolve(bad); !errors.Is(err, project.ErrBadProject) {
			t.Errorf("Resolve(%q): err = %v, want ErrBadProject", bad, err)
		}
	}
}

func TestGitDirNameIsNormalisedButRepoPathIsNot(t *testing.T) {
	clearEnv(t)
	repo := gitRepo(t, "jobflow-ray")
	t.Chdir(repo)
	p, err := project.Resolve("")
	if err != nil || p.Name != "jobflowray" || p.Repo != repo {
		t.Fatalf("got %+v, %v", p, err)
	}
}
