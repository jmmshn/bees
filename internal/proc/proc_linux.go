package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Parent returns pid's parent.
func Parent(pid int) (int, error) {
	f, err := stat(pid)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(f[1])
}

// Start is field 22 of /proc/<pid>/stat, the start time in clock ticks.
func Start(pid int) (string, error) {
	f, err := stat(pid)
	if err != nil {
		return "", err
	}
	if f[0] == "Z" {
		return "", fmt.Errorf("pid %d is a zombie: %w", pid, ErrGone)
	}
	return f[19], nil
}

// stat returns the fields of /proc/<pid>/stat after the command name.
func stat(pid int) ([]string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("pid %d: %w", pid, ErrGone)
	} else if err != nil {
		return nil, err
	}
	// The command name is in parentheses and may contain spaces; fields resume after ')'.
	f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
	if len(f) < 20 {
		return nil, fmt.Errorf("pid %d: short /proc stat", pid)
	}
	return f, nil
}

// Name is pid's command name (/proc/<pid>/comm).
func Name(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if os.IsNotExist(err) {
		return "", fmt.Errorf("pid %d: %w", pid, ErrGone)
	}
	return strings.TrimSpace(string(b)), err
}

// Exe is the path of pid's executable.
func Exe(pid int) (string, error) { return os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)) }

// Descendants returns every live process below pid, with its start time, nearest first.
// A process that exits during the walk is left out; any other read error fails it.
func Descendants(pid int) ([]Process, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var all []Process
	parent := map[int]int{}
	for _, e := range ents {
		n, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a process
		}
		f, err := stat(n)
		if errors.Is(err, ErrGone) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if f[0] == "Z" {
			continue
		}
		pp, err := strconv.Atoi(f[1])
		if err != nil {
			return nil, fmt.Errorf("pid %d: parent %q: %w", n, f[1], err)
		}
		parent[n] = pp
		all = append(all, Process{PID: n, Start: f[19]})
	}
	return descendants(pid, all, parent), nil
}

// Command is pid's command line, its arguments joined by spaces; a kernel thread, which
// has none, reads as its name.
func Command(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if os.IsNotExist(err) {
		return "", fmt.Errorf("pid %d: %w", pid, ErrGone)
	} else if err != nil {
		return "", err
	}
	if s := strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " ")); s != "" {
		return s, nil
	}
	return Name(pid)
}

// Listening returns the TCP ports each of pids listens on: the socket inodes in
// /proc/<pid>/fd matched to the LISTEN rows of /proc/net/tcp and tcp6. A pid that has
// exited listens on nothing.
func Listening(ctx context.Context, pids []int) (map[int][]int, error) {
	port := map[string]int{} // socket inode → port
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(f)
		if os.IsNotExist(err) { // no IPv6
			continue
		} else if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n")[1:] {
			r := strings.Fields(line)
			if len(r) < 10 || r[3] != "0A" { // 0A: TCP_LISTEN
				continue
			}
			_, hex, _ := strings.Cut(r[1], ":")
			n, err := strconv.ParseUint(hex, 16, 16)
			if err != nil {
				return nil, fmt.Errorf("%s: bad local address %q", f, r[1])
			}
			port[r[9]] = int(n)
		}
	}
	out := map[int][]int{}
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		for _, fd := range fds {
			l, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
			if err != nil {
				continue // closed since the listing
			}
			ino, ok := strings.CutPrefix(l, "socket:[")
			if p, listens := port[strings.TrimSuffix(ino, "]")]; ok && listens && !slices.Contains(out[pid], p) {
				out[pid] = append(out[pid], p)
			}
		}
	}
	return out, nil
}

// userHZ is the clock tick of /proc's start times: USER_HZ, 100 on every Linux ABI.
const userHZ = 100

// Age is how long pid has been running.
func Age(pid int) (time.Duration, error) {
	start, err := Start(pid)
	if err != nil {
		return 0, err
	}
	ticks, err := strconv.ParseFloat(start, 64)
	if err != nil {
		return 0, fmt.Errorf("pid %d: start %q: %w", pid, start, err)
	}
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	up, err := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	if err != nil {
		return 0, fmt.Errorf("/proc/uptime: %w", err)
	}
	return time.Duration((up - ticks/userHZ) * float64(time.Second)), nil
}
