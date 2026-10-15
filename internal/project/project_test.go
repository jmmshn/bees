package project_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jmmshn/peons/internal/project"
)

// gitRepo creates a git repo named name under a temp dir and returns its path.
func gitRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	git(t, "", "init", "-q", "-b", "main", dir)
	git(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
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
	for _, k := range []string{"PNS_PROJECT", "PNS_SESSION", "PNS_LOGBOOK", "PNS_REPOS"} {
		t.Setenv(k, "")
	}
}

func TestProjectComesFromGitRepo(t *testing.T) {
	clearEnv(t)
	repo := gitRepo(t, "peons")
	t.Chdir(repo)
	p, err := project.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "peons" || p.Repo != repo {
		t.Fatalf("got %+v, want name peons, repo %s", p, repo)
	}
	if want := filepath.Join(os.Getenv("HOME"), ".local/share/peons/peons.db"); p.Logbook != want {
		t.Fatalf("logbook = %s, want %s", p.Logbook, want)
	}
	if p.Chief() != "chief-peons" {
		t.Fatalf("chief = %s", p.Chief())
	}
}

func TestLinkedWorktreeResolvesToMainRepo(t *testing.T) {
	clearEnv(t)
	repo := gitRepo(t, "peons")
	wt := filepath.Join(filepath.Dir(repo), "peons-WT", "peon-995-8")
	git(t, repo, "worktree", "add", "-q", "-b", "peon-995-8", wt)
	t.Chdir(wt)
	p, err := project.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "peons" || p.Repo != repo {
		t.Fatalf("got %+v, want the main repo %s", p, repo)
	}
}

func TestEnvBeatsGitAndFlagBeatsEnv(t *testing.T) {
	clearEnv(t)
	repos := t.TempDir()
	t.Setenv("PNS_REPOS", repos)
	t.Chdir(gitRepo(t, "peons"))
	t.Setenv("PNS_PROJECT", "dotfiles")
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
	t.Setenv("PNS_LOGBOOK", "/tmp/x.db")
	p, err := project.Resolve("peons")
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
	reg := filepath.Join(os.Getenv("HOME"), ".claude", "sessions")
	if err := os.MkdirAll(reg, 0o755); err != nil {
		t.Fatal(err)
	}
	// The test binary's parent stands in for the Claude process that ran pns.
	body := `{"name":"peon-995-8","pid":` + fmt.Sprint(os.Getppid()) + `}`
	if err := os.WriteFile(filepath.Join(reg, fmt.Sprintf("%d.json", os.Getppid())), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if me, err := project.Me(""); err != nil || me != "peon-995-8" {
		t.Fatalf("ppid walk: %q, %v", me, err)
	}
	t.Setenv("PNS_SESSION", "chief-peons")
	if me, _ := project.Me(""); me != "chief-peons" {
		t.Fatalf("env: %q", me)
	}
	if me, _ := project.Me("captain"); me != "captain" {
		t.Fatalf("flag: %q", me)
	}
}
