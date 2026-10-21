package agy

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/guards"
	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/prompt"
)

func trustAll(string) (bool, error) { return true, nil }

var worker = harness.Start{
	Role: prompt.Worker, Name: "bees-12-12", Dir: "/r-WT/bees-12-12", PromptFile: "/cache/bees-worker.md",
	Doorbell: "[bs] you are bees-12-12 — run: bs inbox",
	Guards:   guards.Rules{Deny: []string{"git push *main*", "gh pr merge*", "it's"}},
}

// bees-12.27: while no home for the guard hook is proven, every start gets ErrUnguarded
// first, whatever else is wrong with it, so no refusal the user can fix hides it.
func TestPrepareRefusesUnguardedBeforeAnyOtherCheck(t *testing.T) {
	untrusted := func(string) (bool, error) { return false, nil }
	st := worker
	st.Continue, st.Path = true, []string{"/build/bin"}
	for _, s := range []harness.Start{st, worker, {Role: prompt.Queen, Dir: "/r"}} {
		if _, err := (Adapter{trusted: untrusted}).Prepare(s); !errors.Is(err, ErrUnguarded) {
			t.Errorf("Prepare(%s) = %v, want ErrUnguarded", s.Role, err)
		}
	}
	if _, err := New(config.Agy{}).Prepare(worker); !errors.Is(err, ErrUnguarded) || !strings.Contains(err.Error(), `harness = "claude_code"`) {
		t.Errorf("New's adapter: err = %v, want ErrUnguarded naming the way out", err)
	}
}

// The worker's launch once guarded: model, its custom agent, the doorbell as the first
// prompt, and never a flag that skips agy's own permission checks.
func TestPrepareRendersTheWorkerLaunch(t *testing.T) {
	l, err := Adapter{cfg: config.Agy{Model: "gemini-3.8-flash-low"}, trusted: trustAll, guarded: true}.Prepare(worker)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--model=gemini-3.8-flash-low", "--agent", "bs-worker", "-i", worker.Doorbell}
	if !slices.Equal(l.Args, want) {
		t.Fatalf("args = %q, want %q", l.Args, want)
	}
	for _, a := range l.Args {
		if strings.Contains(a, "dangerously") {
			t.Errorf("args carry %q", a)
		}
	}
	for key := range l.Files {
		if !harness.FileKey.MatchString(key) {
			t.Errorf("file key %q breaks harness.FileKey", key)
		}
	}
	if want := "---\nname: bs-worker\ndescription: a bees worker, started by bs\nrules:\n  - \"/cache/bees-worker.md\"\n---\n"; l.Files["agent.md"] != want {
		t.Errorf("agent.md = %q, want %q", l.Files["agent.md"], want)
	}
}

func TestPrepareWithoutAModelLeavesAgysDefault(t *testing.T) {
	l, _ := Adapter{trusted: trustAll, guarded: true}.Prepare(harness.Start{Role: prompt.Queen, Dir: "/r", PromptFile: "/cache/q.md"})
	if want := []string{"--agent", "bs-queen"}; !slices.Equal(l.Args, want) {
		t.Fatalf("queen args = %q, want %q", l.Args, want)
	}
	if _, ok := l.Files["hooks.json"]; ok {
		t.Error("the queen got guard hooks")
	}
}

// The hooks.json bs renders runs bs guard with each glob intact through sh -c.
func TestHooksRunBsGuardWithEachGlobQuoted(t *testing.T) {
	l, _ := Adapter{trusted: trustAll, guarded: true}.Prepare(worker)
	var h map[string]struct {
		PreToolUse []struct {
			Matcher string
			Hooks   []struct{ Type, Command string }
		}
	}
	if err := json.Unmarshal([]byte(l.Files["hooks.json"]), &h); err != nil {
		t.Fatal(err)
	}
	pre := h["bs-guards"].PreToolUse
	if len(pre) != 1 || pre[0].Matcher != "run_command" || len(pre[0].Hooks) != 1 || pre[0].Hooks[0].Type != "command" {
		t.Fatalf("hooks.json = %s", l.Files["hooks.json"])
	}
	cmd := pre[0].Hooks[0].Command
	if !strings.HasPrefix(cmd, "bs guard ") {
		t.Fatalf("command = %q", cmd)
	}
	// sh splits the command back into bs guard and exactly the deny globs.
	got, err := exec.Command("sh", "-c", "set -- "+strings.TrimPrefix(cmd, "bs guard ")+`; printf '%s\n' "$@"`).Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(worker.Guards.Deny, "\n") + "\n"; string(got) != want {
		t.Fatalf("sh split the globs into %q, want %q", got, want)
	}
}

// A resume fails visibly: agy's -c picks some other conversation rather than saying none.
func TestPrepareRefusesAResume(t *testing.T) {
	st := worker
	st.Continue = true
	if _, err := (Adapter{trusted: trustAll, guarded: true}).Prepare(st); err == nil || !errors.Is(err, harness.ErrNoResume) {
		t.Fatalf("err = %v, want a resume refusal", err)
	}
}

// --path would silently run the shared bs, so it is refused.
func TestPrepareRefusesPath(t *testing.T) {
	st := worker
	st.Path = []string{"/build/bin"}
	if _, err := (Adapter{trusted: trustAll, guarded: true}).Prepare(st); err == nil || !strings.Contains(err.Error(), "/build/bin") || !strings.Contains(err.Error(), "worker.path") {
		t.Fatalf("err = %v, want a refusal naming the dir, --path and worker.path", err)
	}
}

// An untrusted folder is refused before anything is rendered, naming where to trust.
func TestPrepareRefusesAnUntrustedFolder(t *testing.T) {
	untrusted := func(string) (bool, error) { return false, nil }
	l, err := Adapter{trusted: untrusted, guarded: true}.Prepare(worker)
	if err == nil || !strings.Contains(err.Error(), "hasn't trusted /r-WT/bees-12-12,") || !strings.Contains(err.Error(), "start agy once in that folder") || l.Args != nil {
		t.Fatalf("Prepare = %v, %v; want a trust refusal naming the worktree", l, err)
	}
	broken := errors.New("unreadable")
	if _, err := (Adapter{trusted: func(string) (bool, error) { return false, broken }, guarded: true}).Prepare(worker); !errors.Is(err, broken) {
		t.Fatalf("err = %v, want the trust read's error", err)
	}
}

func TestTrustedReadsAgysListReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if ok, err := Trusted("/r"); ok || err != nil {
		t.Fatalf("no file: %v, %v; want untrusted", ok, err)
	}
	path, _ := TrustFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"permissions":{},"trustedWorkspaces":["/r/bees","/r/bees-WT/"]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]bool{
		"/r/bees": true, "/r/bees/": true, "/r/bees-WT": true,
		"/r/bees/sub": false, "/r/bees-WT/bees-1-2": false, "/r/beesx": false, "/r": false, "/r/bees/../other": false, "r/bees": false,
	} {
		if ok, err := Trusted(dir); ok != want || err != nil {
			t.Errorf("Trusted(%q) = %v, %v; want %v", dir, ok, err, want)
		}
	}
	if b, _ := os.ReadFile(path); string(b) != body {
		t.Errorf("Trusted wrote agy's file: %s", b)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Trusted("/r/bees"); err == nil {
		t.Error("an unreadable list read as an answer")
	}
}

func TestRingIsUnsupported(t *testing.T) {
	if err := (Adapter{}).Ring(harness.Target{Name: "w"}, "hi", harness.Now); !errors.Is(err, harness.ErrNoRing) {
		t.Fatalf("err = %v, want ErrNoRing", err)
	}
}
