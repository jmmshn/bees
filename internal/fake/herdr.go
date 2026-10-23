// Package fake holds the test doubles behind bs's ports (motif 13): a stateful herdr that
// refuses what the real one refuses, gh, cl and beads, and the harness that starts
// beesfake. Each constraint a fake encodes names the probe it came from.
package fake

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/port"
)

// Herdr is a stateful herdr CLI behind port.Runner: argv in, herdr's JSON out. It runs one
// server per --session, each numbering workspaces from w1 and panes from wN:p1 (probed on
// 0.9.5, bees-13.2), and starts each agent as a real process, so liveness, identity and
// the gate run against the real process table. It doubles as that table (Proc), so a
// test can make a lookup fail.
type Herdr struct {
	Home     string            // $HOME: where a --cwd that doesn't exist opens
	Programs map[string]string // agent kind → the program it runs (beesfake, named claude)
	Env      []string          // the env every pane process starts with

	mu      sync.Mutex
	servers map[string]*server
	// Busy is how many next agent starts herdr refuses as agent_pane_busy, typing
	// nothing: a new pane's shell isn't at its prompt yet (bees-12.33).
	Busy int
	// StartFails makes the next agent start fail while its process runs all the same:
	// "timeout" (herdr gave up waiting for ready, bees-12.49) or "lost" (the terminal went
	// away mid-start; the code is not probed).
	StartFails string
	// infoFails is how many next pane process-info calls fail as an unreachable server
	// would: a start confirmation or heal that can't look. FailInfo sets it.
	infoFails int
	// Down: no server runs; every command refuses as server_not_running, and status says
	// "not running" with exit 0 (probed, 0.9.5).
	Down bool
	// Version is the client's version, ServerVersion the running server's (both 0.9.5
	// when unset); Socket is the path status names for the server's socket.
	Version, ServerVersion, Socket string
	// EndpointIncompatible, ProtocolIncompatible: status says the server's endpoint or
	// private protocol isn't the client's; RestartNeeded and BinaryStale: it says
	// update.restart_needed or update.server_binary_stale: yes (0.9.5 says stale with no
	// restart needed; a server version other than the client's is stale too).
	EndpointIncompatible, ProtocolIncompatible, RestartNeeded, BinaryStale bool
	// RefuseNext makes the next call of this command ("workspace report-metadata") fail
	// with herdr's error envelope, as a server-side failure would.
	RefuseNext string

	procFaults
}

type server struct {
	next       int // workspace numbers are never reused
	workspaces []*workspace
}

type workspace struct {
	id, label string
	tokens    map[string]string
	worktree  string // worktree create's path; "" for workspace create
	repo      string
	panes     []*pane
	nextPane  int
}

type pane struct {
	id, cwd string
	agent   *agent
}

// The kinds herdr agent start supports (herdr 0.9.5 --help); any other exits 2.
var kinds = []string{"pi", "claude", "codex", "gemini", "cursor", "devin", "agy", "cline", "omp", "mastracode", "opencode", "copilot", "kimi", "kiro", "droid", "amp", "grok", "hermes", "kilo", "qodercli", "qwen", "letta", "maki", "muse"}

// refusal is herdr's error envelope with exit 1.
func refusal(cmd, code, format string, a ...any) error {
	b, _ := json.Marshal(map[string]any{"id": "cli:" + cmd, "error": map[string]string{"code": code, "message": fmt.Sprintf(format, a...)}})
	return &port.ExitError{Argv: []string{"herdr", cmd}, Code: 1, Stdout: b}
}

// usage is herdr's clap-style refusal: plain text, exit 2.
func usage(format string, a ...any) error {
	return &port.ExitError{Argv: []string{"herdr"}, Code: 2, Stderr: []byte(fmt.Sprintf(format, a...))}
}

// result is herdr's success envelope.
func result(cmd string, r any) ([]byte, error) {
	return json.Marshal(map[string]any{"id": "cli:" + cmd, "result": r})
}

// Run is herdr argv: herdr [--session S] <group> <command> ...
func (h *Herdr) Run(ctx context.Context, dir string, argv ...string) ([]byte, error) {
	if len(argv) == 0 || argv[0] != "herdr" {
		return nil, fmt.Errorf("fake herdr: not a herdr argv: %q", argv)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	args, session := argv[1:], herdr.Default
	if len(args) >= 2 && args[0] == "--session" {
		session, args = args[1], args[2:]
	}
	if len(args) == 1 && args[0] == "status" {
		return h.status(session), nil
	}
	if len(args) < 2 {
		return nil, usage("usage: herdr <group> <command>")
	}
	if h.Down {
		return nil, refusal(strings.Join(args[:2], ":"), "server_not_running", "no herdr server is running for session %s", session)
	}
	s := h.server(session)
	if cmd := args[0] + " " + args[1]; cmd == h.RefuseNext {
		h.RefuseNext = ""
		return nil, refusal(args[0]+":"+args[1], "internal_error", "fake herdr: %s refused", cmd)
	}
	f := map[string]func(*server, string, flags) ([]byte, error){
		"workspace create":          h.workspaceCreate,
		"workspace list":            h.workspaceList,
		"workspace get":             h.workspaceGet,
		"workspace close":           h.workspaceClose,
		"workspace report-metadata": h.reportMetadata,
		"worktree create":           h.worktreeCreate,
		"worktree remove":           h.worktreeRemove,
		"agent start":               h.agentStart,
		"pane list":                 h.paneList,
		"pane process-info":         h.processInfo,
		"pane close":                h.paneClose,
		"pane run":                  h.paneRun,
		"pane move":                 h.paneMove,
	}[args[0]+" "+args[1]]
	if f == nil {
		return nil, usage("fake herdr: no command %q", args[0]+" "+args[1])
	}
	fl, err := parse(args[2:])
	if err != nil {
		return nil, err
	}
	return f(s, session, fl)
}

// status is herdr status's text: the client, then the server of session (0.9.5's layout).
func (h *Herdr) status(session string) []byte {
	v := cmp.Or(h.Version, "0.9.5")
	server := "  status: not running\n"
	if !h.Down {
		sv := cmp.Or(h.ServerVersion, v)
		server = fmt.Sprintf("  status: running\n  version: %s\n  endpoint_compatible: %s\n  private_protocol: 22\n  private_protocol_compatible: %s\n", sv, yes[!h.EndpointIncompatible], yes[!h.ProtocolIncompatible])
	}
	return []byte(fmt.Sprintf("client:\n  version: %s\n  channel: stable\n  protocol: 22\n  endpoint_protocol_generation: 1\n\nserver:\n%s  socket: %s\n\nupdate:\n  restart_needed: %s\n  server_binary_stale: %s\n",
		v, server, h.Socket, yes[h.RestartNeeded], yes[h.BinaryStale || h.ServerVersion != "" && h.ServerVersion != v]))
}

// yes is a bool as herdr status prints it.
var yes = map[bool]string{true: "yes", false: "no"}

// flags is a parsed herdr command line: positionals, repeatable --flags, and what
// follows --.
type flags struct {
	pos  []string
	opts map[string][]string
	rest []string
}

// boolFlags take no value.
var boolFlags = []string{"--no-focus", "--focus", "--force", "--new-tab", "--new-workspace"}

func parse(args []string) (flags, error) {
	f := flags{opts: map[string][]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			f.rest = args[i+1:]
			return f, nil
		case slices.Contains(boolFlags, a):
			f.opts[a] = append(f.opts[a], "")
		case strings.HasPrefix(a, "--"):
			if i+1 >= len(args) {
				return f, usage("error: a value is required for '%s'", a)
			}
			f.opts[a] = append(f.opts[a], args[i+1])
			i++
		default:
			f.pos = append(f.pos, a)
		}
	}
	return f, nil
}

func (f flags) get(name string) (string, bool) {
	v, ok := f.opts[name]
	if !ok {
		return "", false
	}
	return v[len(v)-1], true
}

func (f flags) has(name string) bool { _, ok := f.opts[name]; return ok }

// find returns the workspace with id on s.
func (s *server) find(id string) *workspace {
	for _, w := range s.workspaces {
		if w.id == id {
			return w
		}
	}
	return nil
}

// pane returns the pane with id on s and its workspace.
func (s *server) pane(id string) (*workspace, *pane) {
	for _, w := range s.workspaces {
		for _, p := range w.panes {
			if p.id == id {
				return w, p
			}
		}
	}
	return nil, nil
}

// open adds a workspace with one pane in cwd.
func (s *server) open(label, cwd string) *workspace {
	s.next++
	w := &workspace{id: fmt.Sprintf("w%d", s.next), label: label, tokens: map[string]string{}}
	w.add(cwd)
	s.workspaces = append(s.workspaces, w)
	return w
}

func (w *workspace) add(cwd string) *pane {
	w.nextPane++
	p := &pane{id: fmt.Sprintf("%s:p%d", w.id, w.nextPane), cwd: cwd}
	w.panes = append(w.panes, p)
	return p
}

// drop closes w if it has no pane left, as herdr closes an emptied workspace.
func (s *server) drop(w *workspace) {
	if len(w.panes) == 0 {
		s.workspaces = slices.DeleteFunc(s.workspaces, func(x *workspace) bool { return x == w })
	}
}

func (w *workspace) json() map[string]any {
	m := map[string]any{"workspace_id": w.id, "label": w.label, "pane_count": len(w.panes)}
	if len(w.tokens) > 0 {
		m["tokens"] = w.tokens
	}
	if w.worktree != "" {
		m["worktree"] = map[string]string{"checkout_path": w.worktree}
	}
	return m
}

func (p *pane) json(w *workspace) map[string]any {
	return map[string]any{"pane_id": p.id, "workspace_id": w.id, "cwd": p.cwd, "foreground_cwd": p.cwd}
}

// cwd is where workspace create opens: --cwd if it exists, else $HOME (probed, 0.9.5: a
// missing dir opens $HOME with exit 0); with no --cwd, the focused (first) pane's.
func (h *Herdr) cwd(s *server, f flags) string {
	d, ok := f.get("--cwd")
	if !ok {
		if len(s.workspaces) > 0 {
			return s.workspaces[0].panes[0].cwd
		}
		return h.Home
	}
	if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
		return h.Home
	}
	return d
}
