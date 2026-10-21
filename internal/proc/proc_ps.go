//go:build !linux

package proc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Parent returns pid's parent.
func Parent(pid int) (int, error) {
	out, err := ps(pid, "ppid=")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// Start is the start time exactly as Claude records it on macOS:
// LC_ALL=C TZ=UTC ps -o lstart= -p <pid>, trimmed. Claude skips zombies, and so does Start.
func Start(pid int) (string, error) {
	out, err := ps(pid, "stat=", "lstart=")
	if err != nil {
		return "", err
	}
	state, start, _ := strings.Cut(out, " ")
	if strings.HasPrefix(state, "Z") {
		return "", fmt.Errorf("pid %d is a zombie: %w", pid, ErrGone)
	}
	return strings.TrimSpace(start), nil
}

// psPath is ps by absolute path: a ps earlier on PATH must not answer for bs, whose
// sender check rests on these answers (bees-7.4).
const psPath = "/bin/ps"

// ps prints fields of pid, trimmed. It exits 1 with no output when pid doesn't exist.
func ps(pid int, fields ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	args := []string{}
	for _, f := range fields {
		args = append(args, "-o", f)
	}
	cmd := exec.CommandContext(ctx, psPath, append(args, "-p", strconv.Itoa(pid))...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	s := strings.TrimSpace(string(out))
	switch {
	case err == nil && s != "":
		return s, nil
	case ctx.Err() != nil:
		return "", fmt.Errorf("ps -p %d: %w", pid, ctx.Err())
	case s == "":
		if ee, ok := err.(*exec.ExitError); err == nil || ok && ee.ExitCode() == 1 {
			return "", fmt.Errorf("pid %d: %w", pid, ErrGone)
		}
	}
	return "", fmt.Errorf("ps -p %d: %w", pid, err)
}

// Name is pid's command name: the last element of ps's comm.
func Name(pid int) (string, error) {
	out, err := ps(pid, "comm=")
	if err != nil {
		return "", err
	}
	return filepath.Base(out), nil
}

// Exe is the path ps reports for pid's command.
func Exe(pid int) (string, error) { return ps(pid, "comm=") }

// Descendants returns every live process below pid, with its start time as Start reports
// it, nearest first.
func Descendants(pid int) ([]Process, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, psPath, "-A", "-o", "pid=", "-o", "ppid=", "-o", "stat=", "-o", "lstart=")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ps -A: %w", err)
	}
	var all []Process
	parent := map[int]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f, start := cut3(line)
		if len(f) < 3 || strings.HasPrefix(f[2], "Z") {
			continue
		}
		n, err1 := strconv.Atoi(f[0])
		pp, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("ps -A: bad line %q", line)
		}
		parent[n] = pp
		all = append(all, Process{PID: n, Start: start})
	}
	return descendants(pid, all, parent), nil
}

// cut3 splits a ps line into its first three fields and the rest, trimmed, keeping the
// rest's inner spacing ("Mon Oct  5 …"), as Start returns it.
func cut3(line string) ([]string, string) {
	var f []string
	rest := line
	for range 3 {
		rest = strings.TrimLeft(rest, " ")
		i := strings.IndexByte(rest, ' ')
		if i < 0 {
			return append(f, rest), ""
		}
		f, rest = append(f, rest[:i]), rest[i:]
	}
	return f, strings.TrimSpace(rest)
}
