package fake

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jmmshn/bees/internal/herdr"
	"github.com/jmmshn/bees/internal/proc"
)

// agent is a process herdr started in a pane: a real child of the test, in its own
// process group as a shell job is.
type agent struct {
	name string
	argv []string
	cmd  *exec.Cmd
	done chan struct{} // closed once it has exited and been reaped
}

func (a *agent) alive() bool {
	select {
	case <-a.done:
		return false
	default:
		return true
	}
}

// hangUp ends p's agent as closing its terminal does: SIGHUP to its process group, and
// SIGKILL if it is still there a second later.
func (p *pane) hangUp() {
	a := p.agent
	if a == nil || !a.alive() {
		return
	}
	_ = syscall.Kill(-a.cmd.Process.Pid, syscall.SIGHUP) // gone already: done closes
	select {
	case <-a.done:
	case <-time.After(time.Second):
		_ = syscall.Kill(-a.cmd.Process.Pid, syscall.SIGKILL)
		<-a.done
	}
}

// agentStart is herdr agent start NAME --kind K --pane P [--timeout MS] -- ARGS, refusing
// in herdr's order (probed, 0.9.5): an unknown kind (exit 2, plain text), then any
// argument with a control character (invalid_agent_argument, before the pane lookup),
// then a missing pane, a name a live agent on this server has (agent_name_taken: nothing
// starts), a pane not yet at its shell prompt (agent_pane_busy: nothing typed). Success
// waits for the program to say it is ready (a first line on stdout), as herdr waits for
// the agent to sit at its prompt.
func (h *Herdr) agentStart(s *server, session string, f flags) ([]byte, error) {
	name := first(f.pos)
	kind, _ := f.get("--kind")
	paneID, _ := f.get("--pane")
	if !slices.Contains(kinds, kind) {
		return nil, usage("unsupported interactive agent kind: %s", kind)
	}
	if herdr.CheckAgentName(name) != nil {
		return nil, refusal("agent:start", "invalid_agent_name", "agent name must start with a lowercase letter and contain only lowercase letters, digits, '-' or '_' (1-32 characters)")
	}
	for _, a := range f.rest {
		if herdr.Unsafe(a) {
			return nil, refusal("agent:start", "invalid_agent_argument", "agent argument contains a control character")
		}
	}
	w, p := s.pane(paneID)
	if p == nil {
		return nil, refusal("agent:start", "agent_pane_not_found", "agent target pane %s not found", paneID)
	}
	for _, x := range s.workspaces {
		for _, q := range x.panes {
			if q.agent != nil && q.agent.alive() && q.agent.name == name {
				return nil, refusal("agent:start", "agent_name_taken", "agent name %s is taken", name)
			}
		}
	}
	if p.agent != nil && p.agent.alive() || h.Busy > 0 {
		if h.Busy > 0 {
			h.Busy--
		}
		return nil, refusal("agent:start", "agent_pane_busy", "agent target pane %s is not an available shell", paneID)
	}
	prog := h.Programs[kind]
	if prog == "" {
		return nil, usage("fake herdr: no program for kind %s", kind)
	}
	cmd := exec.Command(prog, f.rest...)
	cmd.Dir = p.cwd
	cmd.Env = h.paneEnv(session, w, p)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, refusal("agent:start", "spawn_failed", "%v", err)
	}
	a := &agent{name: name, argv: append([]string{prog}, f.rest...), cmd: cmd, done: make(chan struct{})}
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(out)
		if sc.Scan() {
			close(ready)
		}
		for sc.Scan() {
		}
		_ = cmd.Wait() // an agent's exit status is not herdr's business
		close(a.done)
	}()
	p.agent = a
	timeout := herdr.StartTimeout
	if ms, ok := f.get("--timeout"); ok {
		if n, err := strconv.Atoi(ms); err == nil {
			timeout = time.Duration(n) * time.Millisecond
		}
	}
	fails := h.StartFails
	h.StartFails = ""
	h.mu.Unlock() // the program may call back into herdr while it starts
	defer h.mu.Lock()
	select {
	case <-ready:
	case <-a.done:
		return nil, refusal("agent:start", "agent_exited", "agent %s exited during startup", name)
	case <-time.After(timeout):
		return nil, refusal("agent:start", "timeout", "agent %s was not ready within %s", name, timeout)
	}
	switch fails {
	case "timeout":
		return nil, refusal("agent:start", "timeout", "agent %s was not ready within %s", name, timeout)
	case "blocked":
		return nil, refusal("agent:start", "agent_not_ready", "agent %s is blocked during startup and is not ready for prompts", name)
	case "lost":
		return nil, refusal("agent:start", "terminal_lost", "the terminal of pane %s went away", paneID)
	}
	return result("agent:start", map[string]any{"type": "agent_started", "pane_id": paneID, "name": name})
}

// processInfo is the pane's shell (the test process stands in for it) and, while its
// agent runs, the agent as the foreground process; else the idle shell. A pane run line's
// sh stands for the shell: the program it started is the foreground one, named as the
// kernel names it (comm, cut to 15 bytes on Linux), as real herdr names it (probed, bees-14.7).
func (h *Herdr) processInfo(s *server, _ string, f flags) ([]byte, error) {
	id, _ := f.get("--pane")
	if h.infoFails > 0 {
		h.infoFails--
		return nil, errors.New("herdr: server not reachable")
	}
	_, p := s.pane(id)
	if p == nil {
		return nil, refusal("pane:process_info", "pane_not_found", "pane %s not found", id)
	}
	shell := os.Getpid()
	fg := []any{map[string]any{"pid": shell, "name": "zsh", "argv": []string{"/usr/bin/zsh"}}}
	if a := p.agent; a != nil && a.alive() {
		pid, argv := a.cmd.Process.Pid, a.argv
		if a.name == "" { // a pane run line: its job, once sh has started one
			if d, err := proc.Descendants(pid); err == nil && len(d) > 0 {
				pid, argv = d[0].PID, cmdline(d[0].PID)
			}
		}
		name, _ := proc.Name(pid)
		fg = []any{map[string]any{"pid": pid, "name": name, "argv": argv}}
	}
	return result("pane:process_info", map[string]any{"type": "pane_process_info", "process_info": map[string]any{
		"pane_id": id, "shell_pid": shell, "foreground_processes": fg}})
}

// cmdline is pid's argv: /proc's where there is one, else ps's command split on spaces.
func cmdline(pid int) []string {
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline"); err == nil {
		return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	}
	c, _ := proc.Command(pid)
	return strings.Fields(c)
}

// Close ends every agent on every server: a test's cleanup.
func (h *Herdr) Close() {
	h.mu.Lock()
	var ps []*pane
	for _, s := range h.servers {
		for _, w := range s.workspaces {
			for _, p := range w.panes {
				ps = append(ps, &pane{id: p.id, agent: p.agent}) // read under mu: agentStart writes agent
			}
		}
	}
	h.mu.Unlock()
	for _, p := range ps {
		p.hangUp()
	}
}

// Agent is the live process herdr started as name on session, for tests that kill it.
func (h *Herdr) Agent(session, name string) (pid int, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, w := range h.server(session).workspaces {
		for _, p := range w.panes {
			if a := p.agent; a != nil && a.alive() && a.name == name {
				return a.cmd.Process.Pid, true
			}
		}
	}
	return 0, false
}

// server is session's server, made on first use. Callers hold mu.
func (h *Herdr) server(session string) *server {
	if h.servers == nil {
		h.servers = map[string]*server{}
	}
	if h.servers[session] == nil {
		h.servers[session] = &server{}
	}
	return h.servers[session]
}

// paneRun is herdr pane run PANE COMMAND...: the arguments, joined with spaces, typed into
// the pane's shell as one line, and an empty answer at once (probed, 0.9.5): here the line
// runs under sh as the pane's foreground process, in its own process group, with the
// pane's env, and the env it leaves is the shell's from then on, as an unset or export
// typed into a real one is (bees-13.13.13). A pane already running something isn't
// modelled (real herdr types into it).
func (h *Herdr) paneRun(s *server, session string, f flags) ([]byte, error) {
	if len(f.pos) < 2 {
		return nil, usage("error: the following required arguments were not provided: <PANE_ID> <COMMAND>...")
	}
	w, p := s.pane(f.pos[0])
	if p == nil {
		return nil, refusal("request", "pane_not_found", "pane %s not found", f.pos[0])
	}
	if p.agent != nil && p.agent.alive() {
		return nil, usage("fake herdr: pane run into pane %s, which runs %v, is not modelled", p.id, p.agent.argv)
	}
	line := strings.Join(f.pos[1:], " ")
	envf, err := os.CreateTemp("", "fakeherdr-env-")
	if err != nil {
		return nil, err
	}
	envf.Close()
	cmd := exec.Command("sh", "-c", line+"\nenv >\"$0\"", envf.Name())
	cmd.Dir = p.cwd
	cmd.Env = h.paneEnv(session, w, p)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		os.Remove(envf.Name())
		return nil, err
	}
	a := &agent{argv: []string{"sh", "-c", line}, cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		if b, err := os.ReadFile(envf.Name()); err == nil && len(b) > 0 {
			h.mu.Lock()
			p.shell = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			h.mu.Unlock()
		}
		os.Remove(envf.Name())
		close(a.done)
	}()
	p.agent = a
	return nil, nil
}

// paneEnv is the env a process in pane p of w on session starts with.
func (h *Herdr) paneEnv(session string, w *workspace, p *pane) []string {
	if p.shell != nil {
		return slices.Clone(p.shell)
	}
	env := append(slices.Clone(h.Env), "HERDR_ENV=1", "HERDR_PANE_ID="+p.id, "HERDR_WORKSPACE_ID="+w.id)
	if session != herdr.Default { // herdr sets HERDR_SESSION only on a named server (probed)
		env = append(env, "HERDR_SESSION="+session)
	}
	return env
}
