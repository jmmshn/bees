// Command beesfake is the harness process the fake herdr starts in a pane: a real program,
// so liveness, identity, the gate and process groups run against the real process table
// (ADR-0010 Tests). It listens on a 0600 ring socket, runs a script of steps a test
// appends to, and starts children on request. Tests build it as "claude", so the pane's
// process has the harness's name.
//
//	beesfake --name N --state DIR   the harness: DIR/ring.sock, DIR/rings.log, DIR/script, DIR/results.log
//	beesfake call ARGS...           one bs call, relayed to the test over $BEESFAKE_RELAY
//	beesfake idle                   a child that runs until killed
//	beesfake detach                 starts an idle child in a new session, prints its pid, exits
//
// A script line is a JSON array: ["bs", args...] runs a bs call as its own job (process
// group), as a shell runs a command, and ["bs|", args...] one piped into a sibling in its
// group (bs done | tail); ["bs&", args...] starts one without waiting (bs queen, which
// execs), its output in a DIR/bg-*.out file the result's stdout names; ["child", tag] starts an idle child in its own
// group, ["child-same-group", tag] one in beesfake's group, ["listen", tag] one listening
// on a TCP port, ["detach", tag] one that escapes the tree; ["kill", tag] kills one;
// ["cd", dir] changes the dir later calls run in; ["exit"] exits. Each step appends one
// JSON line to results.log.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Call is one bs call relayed to the test: what a real bs would see.
type Call struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
	PID  int      `json:"pid"`
	Cwd  string   `json:"cwd"`
}

// Reply is the call's output and exit code, and what to exec after (bs queen).
type Reply struct {
	Stdout  string   `json:"stdout"`
	Stderr  string   `json:"stderr"`
	Code    int      `json:"code"`
	Exec    []string `json:"exec,omitempty"`
	ExecDir string   `json:"exec_dir,omitempty"`
	ExecEnv []string `json:"exec_env,omitempty"`
}

// Result is one script step's outcome.
type Result struct {
	Step   []string `json:"step"`
	PID    int      `json:"pid,omitempty"`
	Stdout string   `json:"stdout,omitempty"`
	Stderr string   `json:"stderr,omitempty"`
	Code   int      `json:"code"`
	Err    string   `json:"error,omitempty"`
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "call":
			os.Exit(call(os.Args[2:]))
		case "idle": // a bare select{} is a deadlock to the runtime, which kills it on macOS
			for {
				time.Sleep(time.Hour)
			}
		case "listen": // idle, listening on a TCP port; says "bound" once it is
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			defer ln.Close()
			fmt.Println("bound")
			for {
				time.Sleep(time.Hour)
			}
		case "detach":
			c := exec.Command(self(), "idle")
			c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := c.Start(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Println(c.Process.Pid)
			os.Exit(0)
		}
	}
	var name, state string
	for i := 1; i+1 < len(os.Args); i++ {
		switch os.Args[i] {
		case "--name":
			name = os.Args[i+1]
		case "--state":
			state = os.Args[i+1]
		}
	}
	if name == "" || state == "" {
		fmt.Fprintln(os.Stderr, "usage: beesfake --name N --state DIR")
		os.Exit(2)
	}
	if err := harness(state); err != nil {
		fmt.Fprintln(os.Stderr, "beesfake:", err)
		os.Exit(1)
	}
}

func self() string {
	exe, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return exe
}

// harness listens for rings and runs the script until told to exit or hung up.
func harness(state string) error {
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	sock := filepath.Join(state, "ring.sock")
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	defer os.Remove(sock)
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}
	go rings(ln, filepath.Join(state, "rings.log"))
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	fmt.Println("ready") // the fake herdr's "agent at its prompt"
	script := filepath.Join(state, "script")
	results := filepath.Join(state, "results.log")
	kids := map[string]int{}
	done := 0
	for {
		select {
		case <-stop:
			return nil
		case <-time.After(20 * time.Millisecond):
		}
		b, _ := os.ReadFile(script)
		lines := strings.Split(string(b), "\n")
		for ; done < len(lines)-1; done++ { // the last element is after the final newline
			var step []string
			if err := json.Unmarshal([]byte(lines[done]), &step); err != nil || len(step) == 0 {
				appendJSON(results, Result{Step: step, Code: -1, Err: fmt.Sprintf("bad step %q", lines[done])})
				continue
			}
			if step[0] == "exit" {
				appendJSON(results, Result{Step: step})
				return nil
			}
			appendJSON(results, run(state, step, kids))
		}
	}
}

// rings appends each ring's text to log, one JSON line per connection.
func rings(ln net.Listener, log string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		b, _ := io.ReadAll(c)
		c.Close()
		appendJSON(log, map[string]string{"ring": string(b)})
	}
}

func run(state string, step []string, kids map[string]int) Result {
	r := Result{Step: step}
	tag := ""
	if len(step) > 1 {
		tag = step[1]
	}
	switch step[0] {
	case "bs", "bs|":
		var out, errb bytes.Buffer
		c := exec.Command(self(), append([]string{"call"}, step[1:]...)...)
		c.Stdout, c.Stderr = &out, &errb
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if step[0] == "bs|" { // bs ... | tail: a sibling in the call's process group, as a shell pipeline has
			sib := exec.Command(self(), "idle")
			sib.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := sib.Start(); err != nil {
				r.Code, r.Err = -1, err.Error()
				break
			}
			defer func() { _ = sib.Process.Kill(); _ = sib.Wait() }()
			c.SysProcAttr.Pgid = sib.Process.Pid
		}
		err := c.Run()
		r.Stdout, r.Stderr = out.String(), errb.String()
		if ee, ok := err.(*exec.ExitError); ok {
			r.Code = ee.ExitCode()
		} else if err != nil {
			r.Code, r.Err = -1, err.Error()
		}
	case "bs&":
		f, err := os.CreateTemp(state, "bg-*.out")
		if err != nil {
			r.Code, r.Err = -1, err.Error()
			break
		}
		defer f.Close() // the child has its own copy
		c := exec.Command(self(), append([]string{"call"}, step[1:]...)...)
		c.Stdout, c.Stderr, r.Stdout = f, f, f.Name()
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			r.Code, r.Err = -1, err.Error()
			break
		}
		go func() { _ = c.Wait() }() // reap it
		r.PID = c.Process.Pid
	case "cd":
		if err := os.Chdir(tag); err != nil {
			r.Code, r.Err = -1, err.Error()
		}
	case "listen":
		c := exec.Command(self(), "listen")
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		out, err := c.StdoutPipe()
		if err == nil {
			err = c.Start()
		}
		if err != nil {
			r.Code, r.Err = -1, err.Error()
			break
		}
		// The step's result waits for the port: a gate checked after it sees it bound.
		if _, err := bufio.NewReader(out).ReadString('\n'); err != nil {
			r.Code, r.Err = -1, "listen: "+err.Error()
		}
		go func() { _ = c.Wait() }()
		kids[tag], r.PID = c.Process.Pid, c.Process.Pid
	case "child", "child-same-group":
		c := exec.Command(self(), "idle")
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: step[0] == "child"}
		if err := c.Start(); err != nil {
			r.Code, r.Err = -1, err.Error()
			break
		}
		go func() { _ = c.Wait() }() // reap it
		kids[tag], r.PID = c.Process.Pid, c.Process.Pid
	case "detach":
		out, err := exec.Command(self(), "detach").Output()
		pid, perr := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil || perr != nil {
			r.Code, r.Err = -1, fmt.Sprint(err, perr)
			break
		}
		kids[tag], r.PID = pid, pid
	case "kill":
		r.PID = kids[tag]
		if err := syscall.Kill(kids[tag], syscall.SIGKILL); err != nil {
			r.Code, r.Err = -1, err.Error()
		}
	default:
		r.Code, r.Err = -1, "unknown step "+step[0]
	}
	return r
}

func appendJSON(path string, v any) {
	b, _ := json.Marshal(v)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n')) // a lost log line fails the test that reads it
}

// call relays one bs call to the test and exits with its code.
func call(argv []string) int {
	conn, err := net.Dial("unix", os.Getenv("BEESFAKE_RELAY"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "beesfake call: no relay:", err)
		return 125
	}
	defer conn.Close()
	cwd, _ := os.Getwd()
	if err := json.NewEncoder(conn).Encode(Call{Argv: argv, Env: os.Environ(), PID: os.Getpid(), Cwd: cwd}); err != nil {
		fmt.Fprintln(os.Stderr, "beesfake call:", err)
		return 125
	}
	var r Reply
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&r); err != nil {
		fmt.Fprintln(os.Stderr, "beesfake call: no reply:", err)
		return 125
	}
	fmt.Print(r.Stdout)
	fmt.Fprint(os.Stderr, r.Stderr)
	if r.Code == 0 && len(r.Exec) > 0 {
		if err := os.Chdir(r.ExecDir); err != nil {
			fmt.Fprintln(os.Stderr, "beesfake call:", err)
			return 125
		}
		err := syscall.Exec(r.Exec[0], r.Exec, r.ExecEnv)
		fmt.Fprintln(os.Stderr, "beesfake call: exec:", err)
		return 125
	}
	return r.Code
}
