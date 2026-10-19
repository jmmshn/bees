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

func clearEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"BS_PROJECT", "BS_SESSION", "BS_LOGBOOK", "BS_REPOS", "CLAUDE_PID", "CLAUDE_CODE_SESSION_ID"} {
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
	wt := filepath.Join(filepath.Dir(repo), "bees-WT", "beads-995-8")
	git(t, repo, "worktree", "add", "-q", "-b", "beads-995-8", wt)
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
	t.Setenv("BS_REPOS", t.TempDir())
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

func TestNamedProjectNeedsReposOutsideItsGitRepo(t *testing.T) {
	clearEnv(t)
	t.Chdir(t.TempDir())
	p, err := project.Resolve("bees")
	if err != nil || p.Repo != "" {
		t.Fatalf("outside git: got %+v, %v; want no repo and no error (messages need only the logbook)", p, err)
	}
	if err := p.NeedRepo(); !errors.Is(err, project.ErrNoRepos) {
		t.Fatalf("outside git: NeedRepo = %v, want ErrNoRepos", err)
	}
	t.Chdir(gitRepo(t, "dotfiles"))
	if p, _ := project.Resolve("bees"); !errors.Is(p.NeedRepo(), project.ErrNoRepos) {
		t.Fatalf("in another project's repo: got %+v, want ErrNoRepos", p)
	}
}

func TestNamedProjectUsesItsOwnGitRepo(t *testing.T) {
	clearEnv(t)
	repo := gitRepo(t, "jobflow-ray")
	t.Chdir(repo)
	t.Setenv("BS_PROJECT", "jobflowray") // as in every worker's settings
	p, err := project.Resolve("")
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

// A worker's settings carry BS_PROJECT. In its own worktree that beats the git repo, which
// names another project in a --repo worktree (bees-1.54). In some other repo it would
// silently file into the worker's project, so bs refuses and says how to pick (bees-8.7).
func TestInheritedProjectYieldsToAnotherRepo(t *testing.T) {
	clearEnv(t)
	own := gitRepo(t, "jobflow-ray") // a --repo worker's worktree: another project's repo
	register(t, "bees-8-1", own)
	t.Setenv("BS_PROJECT", "bees")
	t.Chdir(own)
	if p, err := project.Resolve(""); err != nil || p.Name != "bees" {
		t.Fatalf("own worktree: %+v, %v; want bees", p, err)
	}
	t.Chdir(gitRepo(t, "bsdemo"))
	if p, err := project.Resolve(""); !errors.Is(err, project.ErrWrongProject) || !strings.Contains(err.Error(), "--project bsdemo") {
		t.Fatalf("another repo: %+v, %v; want ErrWrongProject naming --project bsdemo", p, err)
	}
	if p, err := project.Resolve("bsdemo"); err != nil || p.Name != "bsdemo" {
		t.Fatalf("--project bsdemo: %+v, %v", p, err)
	}
	if p, err := project.Resolve("bees"); err != nil || p.Name != "bees" {
		t.Fatalf("--project bees: %+v, %v", p, err)
	}
	// A submodule inside the session's own checkout is still the session's work: its git
	// common dir lives in .git/modules, which must not read as a project "modules".
	sub := gitRepo(t, "vendored")
	git(t, own, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "lib/vendored")
	t.Chdir(filepath.Join(own, "lib", "vendored"))
	if p, err := project.Resolve(""); err != nil || p.Name != "bees" {
		t.Fatalf("submodule of the own checkout: %+v, %v; want bees", p, err)
	}
	// Suggestions are only names that would pass: a repo called 2fa-app isn't one.
	t.Chdir(gitRepo(t, "2fa-app"))
	if _, err := project.Resolve(""); !errors.Is(err, project.ErrWrongProject) || strings.Contains(err.Error(), "2faapp") || !strings.Contains(err.Error(), "--project bees") {
		t.Fatalf("invalid repo name: %v; want ErrWrongProject suggesting only --project bees", err)
	}
	register(t, "bees-8-1", "") // fail closed: no launch directory means no repo is the session's own
	t.Chdir(own)
	if _, err := project.Resolve(""); !errors.Is(err, project.ErrWrongProject) {
		t.Fatalf("empty registry cwd: %v; want ErrWrongProject", err)
	}
	clearEnv(t) // outside Claude, a BS_PROJECT the keeper set still wins
	t.Setenv("BS_PROJECT", "bees")
	if p, err := project.Resolve(""); err != nil || p.Name != "bees" {
		t.Fatalf("outside Claude: %+v, %v; want bees", p, err)
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

// A backgrounded bs is reparented to init, so no Claude ancestor is left to walk to; the
// session env Claude's Bash tool exports, which the orphan inherits, still names it (bees-8.8).
func TestMeFindsTheSessionOfAnOrphanedBsFromItsEnv(t *testing.T) {
	clearEnv(t)
	reg := filepath.Join(os.Getenv("HOME"), ".claude", "sessions")
	if err := os.MkdirAll(reg, 0o755); err != nil {
		t.Fatal(err)
	}
	// This test process is no ancestor of itself, so only the env can lead to it.
	pid := os.Getpid()
	start, err := proc.Start(pid)
	if err != nil {
		t.Fatal(err)
	}
	writeEntry := func(sessionID, procStart string) {
		body := fmt.Sprintf(`{"name":"bees-8-8","pid":%d,"sessionId":%q,"procStart":%q}`, pid, sessionID, procStart)
		if err := os.WriteFile(filepath.Join(reg, fmt.Sprintf("%d.json", pid)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeEntry("sid-1", start)
	t.Setenv("CLAUDE_PID", fmt.Sprint(pid))
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-1")
	if me, err := project.Me(""); err != nil || me != "bees-8-8" {
		t.Fatalf("orphan: %q, %v; want bees-8-8", me, err)
	}
	if me, err := project.Me("zz-spoof"); !errors.Is(err, project.ErrNotYou) {
		t.Fatalf("orphan --as another: %q, %v; want ErrNotYou", me, err)
	}
	// An env that doesn't match a live entry (leaked from another session, or a session
	// whose pid was reused) fails closed: falling through to "not in Claude" would let
	// --as name anyone.
	for name, edit := range map[string]func(){
		"other session id": func() { writeEntry("sid-2", start) },
		"stale procStart":  func() { writeEntry("sid-1", "Mon Jan  1 00:00:00 2001") },
		"no session id":    func() { writeEntry("", start) },
		"no entry":         func() { os.Remove(filepath.Join(reg, fmt.Sprintf("%d.json", pid))) },
		"no CLAUDE_CODE_SESSION_ID": func() {
			writeEntry("sid-1", start)
			t.Setenv("CLAUDE_CODE_SESSION_ID", "")
		},
	} {
		edit()
		if me, err := project.Me("keeper"); !errors.Is(err, project.ErrStaleSession) {
			t.Errorf("%s: %q, %v; want ErrStaleSession", name, me, err)
		}
		writeEntry("sid-1", start)
		t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-1")
	}
	// A Claude ancestor beats the env: a worker can't forge the queen's pid and session id.
	ppStart, err := proc.Start(os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"name":"bees-8-9","pid":%d,"procStart":%q}`, os.Getppid(), ppStart)
	if err := os.WriteFile(filepath.Join(reg, fmt.Sprintf("%d.json", os.Getppid())), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if me, err := project.Me(""); err != nil || me != "bees-8-9" {
		t.Fatalf("ancestor vs forged env: %q, %v; want bees-8-9", me, err)
	}
	if me, err := project.Me("bees-8-8"); !errors.Is(err, project.ErrNotYou) {
		t.Fatalf("--as the forged session: %q, %v; want ErrNotYou", me, err)
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

func TestRepoPathFromNameFindsTheDirWhoseNormalisedNameMatches(t *testing.T) {
	clearEnv(t)
	repos := t.TempDir()
	t.Setenv("BS_REPOS", repos)
	want := filepath.Join(repos, "pymatgen-analysis-defects")
	if err := os.Mkdir(want, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pymatgen-analysis-defects", "pymatgenanalysisdefects", "Pymatgen_Analysis_Defects"} {
		if p, err := project.Resolve(name); err != nil || p.Repo != want {
			t.Errorf("Resolve(%q) = %+v, %v; want Repo %s", name, p, err, want)
		}
	}
}
