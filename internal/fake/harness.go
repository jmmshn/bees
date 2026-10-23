package fake

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/port"
)

var build struct {
	once sync.Once
	path string
	err  error
}

// Beesfake builds beesfake once per test binary, named claude so its process has the
// harness's name, and returns its path. A bs beside it is beesfake too: a bs call relayed
// to the test, for a pane whose shell runs bs (the queen's auto-start).
func Beesfake(t testing.TB) string {
	t.Helper()
	build.once.Do(func() {
		dir, err := os.MkdirTemp("", "beesfake")
		if err != nil {
			build.err = err
			return
		}
		build.path = filepath.Join(dir, "claude")
		out, err := exec.Command("go", "build", "-o", build.path, "github.com/jmmshn/bees/internal/fake/beesfake").CombinedOutput()
		if err != nil {
			build.err = fmt.Errorf("go build beesfake: %w: %s", err, out)
		} else if err := os.Symlink("claude", filepath.Join(dir, "bs")); err != nil { // the shared bs on a pane's PATH
			build.err = err
		}
	})
	if build.err != nil {
		t.Fatal(build.err)
	}
	return build.path
}

// RemoveBuild removes Beesfake's build: a TestMain's, after its tests ran.
func RemoveBuild() {
	if build.path != "" {
		os.RemoveAll(filepath.Dir(build.path))
	}
}

// Harness is the harness adapter for beesfake: kind claude, a state dir per session
// under State (short, so its socket path fits sun_path).
type Harness struct{ State string }

func (Harness) Kind() string { return "claude" }

// Dir is the state dir of session name.
func (h Harness) Dir(name string) string { return filepath.Join(h.State, name) }

// Prepare is pure: the argv names the session and its state dir; Record keeps the state
// dir and the PATH dirs it was given, for tests to read back from the spawned entry. A
// worker's guards are .agents/deny.json in its per-start dir, as agy's hooks are.
func (h Harness) Prepare(st port.Start) (port.Launch, error) {
	if st.Name == "" {
		return port.Launch{}, errors.New("beesfake: no name")
	}
	l := port.Launch{Args: []string{"--name", st.Name, "--state", h.Dir(st.Name)},
		Record: map[string]string{"state": h.Dir(st.Name), "path": strings.Join(st.Path, ":")}}
	// The prompt file and doorbell go on the line as Claude's do, so the launch's length is
	// the real one's (beesfake ignores them).
	if st.PromptFile != "" {
		l.Args = append(l.Args, "--append-system-prompt-file", st.PromptFile)
	}
	if st.Doorbell != "" {
		l.Args = append(l.Args, st.Doorbell)
	}
	if st.Role == port.Worker {
		b, err := json.Marshal(st.Deny)
		if err != nil {
			return l, err
		}
		l.Dir = map[string]string{".agents/deny.json": string(b)}
		l.Args = append(l.Args, "--add-dir", "{{dir}}")
	}
	return l, nil
}

// Ring writes the doorbell to the session's 0600 ring socket; no listener is
// ErrNotRunning.
func (h Harness) Ring(t port.Target, text string, p port.Priority) error {
	c, err := net.Dial("unix", filepath.Join(h.Dir(t.Name), "ring.sock"))
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("%s: %w", t.Name, port.ErrNotRunning)
	}
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = fmt.Fprintf(c, "%s\t%s", p, text)
	return err
}

// Step appends one script step for session name's beesfake.
func (h Harness) Step(t testing.TB, name string, step ...string) {
	t.Helper()
	b, _ := json.Marshal(step)
	f, err := os.OpenFile(filepath.Join(h.Dir(name), "script"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

// Result is one beesfake step's outcome (beesfake's Result).
type Result struct {
	Step   []string `json:"step"`
	PID    int      `json:"pid"`
	Stdout string   `json:"stdout"`
	Stderr string   `json:"stderr"`
	Code   int      `json:"code"`
	Err    string   `json:"error"`
}

// Await waits for session name's nth step result (from 1) and returns it.
func (h Harness) Await(t testing.TB, name string, n int) Result {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		lines := readLines(filepath.Join(h.Dir(name), "results.log"))
		if len(lines) >= n {
			var r Result
			if err := json.Unmarshal([]byte(lines[n-1]), &r); err != nil {
				t.Fatal(err)
			}
			if r.PID > 0 && r.Step[0] != "kill" { // a child, detached ones too: the test's to end
				t.Cleanup(func() { _ = syscall.Kill(r.PID, syscall.SIGKILL) })
			}
			return r
		}
	}
	t.Fatalf("%s: no result %d of its script", name, n)
	return Result{}
}

// Rings is every ring session name's beesfake took, oldest first.
func (h Harness) Rings(name string) []string {
	var out []string
	for _, l := range readLines(filepath.Join(h.Dir(name), "rings.log")) {
		var r struct{ Ring string }
		if json.Unmarshal([]byte(l), &r) == nil {
			out = append(out, r.Ring)
		}
	}
	return out
}

func readLines(path string) []string {
	b, _ := os.ReadFile(path)
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// Call is a bs call a beesfake relayed: the argv, the env and pid of the real process
// that made it (a child of the harness process), and its cwd.
type Call struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
	PID  int      `json:"pid"`
	Cwd  string   `json:"cwd"`
}

// Reply is what the relayed call prints and exits with; with Exec, the caller then
// execs it, in ExecDir with ExecEnv, as bs queen's last step does.
type Reply struct {
	Stdout  string   `json:"stdout"`
	Stderr  string   `json:"stderr"`
	Code    int      `json:"code"`
	Exec    []string `json:"exec,omitempty"`
	ExecDir string   `json:"exec_dir,omitempty"`
	ExecEnv []string `json:"exec_env,omitempty"`
}

// Relay serves beesfake calls at path with handle until the test ends; pane processes
// find it through BEESFAKE_RELAY=path.
func Relay(t testing.TB, path string, handle func(Call) Reply) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var call Call
				if err := json.NewDecoder(bufio.NewReader(c)).Decode(&call); err != nil {
					return
				}
				_ = json.NewEncoder(c).Encode(handle(call)) // a caller gone mid-call gets nothing
			}()
		}
	}()
}
