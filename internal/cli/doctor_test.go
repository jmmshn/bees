package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/fake"
	"github.com/jmmshn/bees/internal/harness/claude"
	"github.com/jmmshn/bees/internal/port"
)

// doctorRig is a rig whose PATH starts with stub claude, bd, gh and herdr programs, on a
// fake herdr whose socket is a file of the test's.
type doctorRig struct {
	*rig
	bin     string
	herdr   *fake.Herdr
	socket  string
	cantRun string // a subcommand (auth, where) whose run fails before it starts
}

func newDoctorRig(t *testing.T) *doctorRig {
	r := &doctorRig{rig: newRig(t), bin: t.TempDir()}
	r.stub("claude", `echo "2.1.307 (Claude Code)"`)
	r.stub("bd", `case $1 in --version) echo "bd version 1.1.2";; where) echo /store/.beads;; esac`)
	r.stub("gh", `case $1 in --version) echo "gh version 2.80.0 (2025-01-01)";; auth) exit 0;; esac`)
	r.stub("herdr", "exit 9") // found on PATH; its calls go to the fake
	r.socket = filepath.Join(t.TempDir(), "herdr.sock")
	if err := os.WriteFile(r.socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.herdr = &fake.Herdr{Home: r.home, Socket: r.socket}
	t.Cleanup(r.herdr.Close)
	if err := os.MkdirAll(filepath.Join(r.home, ".claude", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// git alone, linked in: a dir like /usr/bin would bring a real gh or bd along.
	if err := os.Symlink(git, filepath.Join(r.bin, "git")); err != nil {
		t.Fatal(err)
	}
	r.env = append(r.env, "PATH="+r.bin)
	return r
}

func (r *doctorRig) stub(name, body string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		r.t.Fatal(err)
	}
}

// doctor runs bs doctor in the repo, with extra env.
func (r *doctorRig) doctor(extra ...string) (int, string) {
	r.t.Helper()
	return r.doctorIn(r.repo, extra...)
}

// cantStart fails a command whose argv[1] is sub as if it never started: not an exit.
type cantStart struct {
	port.Runner
	sub string
}

func (c cantStart) Run(ctx context.Context, dir string, argv ...string) ([]byte, error) {
	if c.sub != "" && len(argv) > 1 && argv[1] == c.sub {
		return nil, errors.New("fork/exec: resource temporarily unavailable")
	}
	return c.Runner.Run(ctx, dir, argv...)
}

// doctorIn runs bs doctor in dir, with extra env.
func (r *doctorRig) doctorIn(dir string, extra ...string) (int, string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	ports := Ports{Run: cantStart{fake.Commands{Herdr: r.herdr, Real: port.Exec{}}, r.cantRun}, CacheDir: filepath.Join(r.home, "cache"),
		NewHarness: func(string, string) port.Harness { return claude.Adapter{} }}
	code := Run(context.Background(), Env{Vars: append(append([]string{}, r.env...), extra...), Dir: dir, Self: os.Getpid()}, ports, []string{"doctor"}, &out, &errb)
	return code, out.String() + errb.String()
}

func TestDoctorPasses(t *testing.T) {
	r := newDoctorRig(t)
	code, out := r.doctor()
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	wantIn(t, out, "ok   config: herdr session toy: global "+filepath.Join(r.session, "config.toml"),
		"ok   project: mytoy (the git repo at "+r.repo+"), repo "+r.repo+", workspace git_worktree",
		"ok   logbook dir: "+r.logbook,
		"ok   harness claude_code: "+filepath.Join(r.bin, "claude")+" 2.1.307",
		"ok   ring sockets: "+filepath.Join(r.home, ".claude", "sessions")+", 0 ring sockets and their dirs, 0 key files: none open to group or others",
		"ok   herdr session toy: server 0.9.5 running, client 0.9.5",
		"ok   herdr socket: "+r.socket+" is the user's alone",
		"ok   bd: "+filepath.Join(r.bin, "bd")+" 1.1.2, store /store/.beads",
		"ok   gh: ",
		// bees-12.42: no GitHub remote fails nothing until a pr goal, so it's said here.
		"WARN remote: "+r.repo+" has no GitHub remote: bs spawn refuses --goal pr", "ok   env: no BS_* variables set")
	if err := exec.Command("git", "-C", r.repo, "remote", "add", "origin", "git@github.com:o/toy.git").Run(); err != nil {
		t.Fatal(err)
	}
	if _, out := r.doctor(); !strings.Contains(out, "ok   remote: o/toy for pr goals") {
		t.Errorf("with a GitHub remote:\n%s", out)
	}
	r.stub("gh", `case $1 in --version) echo "gh version 2.80.0";; auth) echo "not logged in" >&2; exit 1;; esac`)
	if _, out := r.doctor(); !strings.Contains(out, "WARN gh: "+filepath.Join(r.bin, "gh")+" 2.80.0, but not logged in") {
		t.Errorf("gh logged out:\n%s", out)
	}
	// Rule G: gh that couldn't run is never "not logged in".
	r.cantRun = "auth"
	if _, out := r.doctor(); !strings.Contains(out, "WARN gh: "+filepath.Join(r.bin, "gh")+" 2.80.0, but gh auth status couldn't run") {
		t.Errorf("gh auth status can't run:\n%s", out)
	}
	r.cantRun = ""
	os.Remove(filepath.Join(r.bin, "gh"))
	if _, out := r.doctor(); !strings.Contains(out, "WARN gh: gh is not on PATH: install it") {
		t.Errorf("no gh:\n%s", out)
	}
}

// bees-12.44, 12.48: a herdr server older than its client is said, with how to restart
// it; an incompatible one or none running fails.
func TestDoctorHerdrServer(t *testing.T) {
	r := newDoctorRig(t)
	r.herdr.ServerVersion = "0.9.3"
	code, out := r.doctor()
	if code != 0 {
		t.Errorf("an older server: exit %d", code)
	}
	wantIn(t, out, "WARN herdr session toy: server 0.9.3 is older than client 0.9.5", "herdr --session toy server stop")
	r.herdr.ServerVersion, r.herdr.RestartNeeded = "", true
	if _, out := r.doctor(); !strings.Contains(out, "WARN herdr session toy: herdr says its server needs a restart") {
		t.Errorf("restart needed:\n%s", out)
	}
	// 0.9.5 says server_binary_stale with no restart needed.
	r.herdr.RestartNeeded, r.herdr.BinaryStale = false, true
	if _, out := r.doctor(); !strings.Contains(out, "WARN herdr session toy: herdr says its server runs a stale binary") {
		t.Errorf("stale binary:\n%s", out)
	}
	r.herdr.BinaryStale = false
	// Each protocol alone fails (motif 13: the fake reports them apart, as herdr does).
	r.herdr.ServerVersion, r.herdr.EndpointIncompatible = "0.9.3", true
	if code, out := r.doctor(); code != 1 || !strings.Contains(out, "FAIL herdr session toy: client 0.9.5 can't reach server 0.9.3's endpoint") {
		t.Errorf("endpoint incompatible: exit %d\n%s", code, out)
	}
	r.herdr.EndpointIncompatible, r.herdr.ProtocolIncompatible = false, true
	if code, out := r.doctor(); code != 1 || !strings.Contains(out, "FAIL herdr session toy: client 0.9.5 doesn't speak server 0.9.3's private protocol: restart the server (herdr --session toy server stop") {
		t.Errorf("private protocol incompatible: exit %d\n%s", code, out)
	}
	r.herdr.ProtocolIncompatible = false
	r.herdr.Down = true
	if code, out := r.doctor(); code != 1 || !strings.Contains(out, "FAIL herdr session toy: server not running (socket "+r.socket+"): start it with this build first on its PATH, PATH=") {
		t.Errorf("down: exit %d\n%s", code, out)
	}
}

// T3: a socket or socket dir open to group or others fails.
func TestDoctorSocketModes(t *testing.T) {
	r := newDoctorRig(t)
	if err := os.Chmod(r.socket, 0o666); err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(r.home, ".claude", "sessions")
	ring := filepath.Join(t.TempDir(), "cc-socks")
	if err := os.Mkdir(ring, 0o755); err != nil {
		t.Fatal(err)
	}
	reg := `{"pid":1,"messagingSocketPath":"` + filepath.Join(ring, "1.sock") + `"}`
	if err := os.WriteFile(filepath.Join(sessions, "1.json"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := r.doctor()
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	wantIn(t, out, "FAIL herdr socket: "+r.socket+" is mode 0666", "FAIL ring sockets: "+ring+" is mode 0755")
	if err := os.Chmod(ring, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "1.x.key"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, out := r.doctor(); !strings.Contains(out, "FAIL ring sockets: "+filepath.Join(sessions, "1.x.key")+" is mode 0644") {
		t.Errorf("a key file open to others:\n%s", out)
	}
}

// Tools missing or too old fail, naming the oldest bs works with. A config argv[0] not on
// PATH is a WARN while bs doesn't read its key (checks: bees-13.11), and a relative one
// is found from the repo top, wherever doctor runs.
func TestDoctorTools(t *testing.T) {
	r := newDoctorRig(t)
	r.stub("claude", `echo "2.1.200 (Claude Code)"`)
	os.Remove(filepath.Join(r.bin, "bd"))
	r.write("config.toml", "[paths]\nlogbook_dir = \""+r.logbook+"\"\n[checks.sent]\nrun = [\"cl-status\", \"{name}\"]\n[checks.local]\nrun = [\"./scripts/chk\"]\n")
	if err := os.MkdirAll(filepath.Join(r.repo, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.repo, "scripts", "chk"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(r.repo, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	code, out := r.doctorIn(sub)
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	wantIn(t, out, "FAIL harness claude_code: "+filepath.Join(r.bin, "claude")+" 2.1.200 is older than 2.1.296",
		"FAIL bd: bd is not on PATH", "WARN checks.sent.run: cl-status is not on this shell's PATH (a check runs on the PATH of the bs that runs it", "bs doesn't read checks.sent.run yet")
	if strings.Contains(out, "checks.local.run: ") || strings.Contains(out, "FAIL checks") {
		t.Errorf("./scripts/chk from the repo top, or an unread key failed:\n%s", out)
	}
	r.stub("cl-status", "exit 0")
	if _, out := r.doctor(); !strings.Contains(out, "ok   commands: 2 argv in the config, each argv[0] on this shell's PATH") {
		t.Errorf("with cl-status:\n%s", out)
	}
}

// Rule G: bd that couldn't run is never "no store".
func TestDoctorBdCantRun(t *testing.T) {
	r := newDoctorRig(t)
	r.cantRun = "where"
	if code, out := r.doctor(); code != 1 || !strings.Contains(out, "FAIL bd: "+filepath.Join(r.bin, "bd")+" 1.1.2, but bd where couldn't run from "+r.repo) {
		t.Errorf("exit %d\n%s", code, out)
	}
}

// Rule G: with no config dir doctor fails that line and says what writes it.
func TestDoctorNoConfigDir(t *testing.T) {
	r := newDoctorRig(t)
	os.RemoveAll(r.session)
	code, out := r.doctor()
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	wantIn(t, out, "FAIL config dir: "+r.session+" is missing for herdr session toy: bs config init writes it")
}

// A workspace kind spawn refuses until bees-13.10/13.11 fails doctor, never an ok line;
// [workspace.command] isn't called ignored, since spawn refuses it.
func TestDoctorFailsAKindSpawnRefuses(t *testing.T) {
	r := newDoctorRig(t)
	for body, want := range map[string]string{
		"[workspace]\nkind = \"none\"\n":           "FAIL workspace: workspace.kind = none over a git checkout",
		"[workspace.command]\ncreate = [\"jj\"]\n": "FAIL workspace: command ([workspace.command] is set",
	} {
		r.write("projects/mytoy.toml", body)
		code, out := r.doctor()
		if code != 1 || !strings.Contains(out, want) || strings.Contains(out, "bs ignores it") {
			t.Errorf("%q: exit %d\n%s", body, code, out)
		}
	}
}

// With the project refused, doctor says which checks it skipped (Rule G).
func TestDoctorSaysWhatItSkipped(t *testing.T) {
	r := newDoctorRig(t)
	code, out := r.doctor("BS_PROJECT=bees")
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	wantIn(t, out, "FAIL project: BS_PROJECT=bees", "skip project: project, workspace and logbook dir", "skip review and commands: gh and the GitHub remote")
}

// A flag a command doesn't know is a refusal, exit 2, never a failure.
func TestUnknownFlagIsRefusal(t *testing.T) {
	r := newRig(t)
	r.start()
	for _, args := range [][]string{{"config", "init", "--bogus"}, {"inbox", "--bogus"}} {
		if code, _, stderr := r.run(r.repo, nil, args...); code != 2 || !strings.Contains(stderr, "flag provided but not defined") {
			t.Errorf("%v: exit %d %s", args, code, stderr)
		}
	}
}

// bees-14.1: harness agy checks agy.bin, refusing one that's missing; a worker's agy is
// the one herdr finds on PATH, so a bin path that isn't it is said, and none fails.
func TestDoctorAgyBin(t *testing.T) {
	r := newDoctorRig(t)
	opt := filepath.Join(t.TempDir(), "agy")
	r.write("config.toml", "harness = \"agy\"\n[agy]\nbin = \""+opt+"\"\n[paths]\nlogbook_dir = \""+r.logbook+"\"\n")
	code, out := r.doctor()
	if code != 1 {
		t.Errorf("no agy at %s: exit %d", opt, code)
	}
	wantIn(t, out, "FAIL harness agy: "+opt+" is missing or not executable")
	if err := os.WriteFile(opt, []byte("#!/bin/sh\necho 1.4.6\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, out = r.doctor()
	wantIn(t, out, "ok   harness agy: "+opt+" 1.4.6", "FAIL agy.bin: workers start agy from PATH (herdr's --kind agy), and this shell's PATH has none")
	r.stub("agy", "echo 1.4.6")
	_, out = r.doctor()
	wantIn(t, out, "WARN agy.bin: workers start "+filepath.Join(r.bin, "agy")+", the agy on this shell's PATH (herdr's --kind agy), not "+opt)
	os.Remove(filepath.Join(r.bin, "agy"))
	if err := os.Symlink(opt, filepath.Join(r.bin, "agy")); err != nil {
		t.Fatal(err)
	}
	if _, out = r.doctor(); strings.Contains(out, "agy.bin") {
		t.Errorf("PATH's agy links to agy.bin:\n%s", out)
	}
	r.write("config.toml", "harness = \"agy\"\n[paths]\nlogbook_dir = \""+r.logbook+"\"\n")
	if _, out = r.doctor(); strings.Contains(out, "agy.bin") || !strings.Contains(out, "ok   harness agy: "+filepath.Join(r.bin, "agy")+" 1.4.6") {
		t.Errorf("agy on PATH:\n%s", out)
	}
}
