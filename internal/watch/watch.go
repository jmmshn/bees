// Package watch is a session's mail watcher lock (bees-13.13.10): bs inbox --wait holds
// it while it waits, so a second watcher for the same session refuses at once, and a
// ring or the gate asks it whether one is armed. The lock is the live process's own
// (flock), never a record: it goes when the watcher does, however it ends (motif 9).
package watch

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Path is session name's watcher lock in cache dir: <name>-watch-lock, one of the
// <name>-<stem>-* files bs removes at end, prune and before each start.
func Path(cache, name string) string { return filepath.Join(cache, name+"-watch-lock") }

// Arm takes the lock at path for the watcher pid. held is the pid of the watcher already
// holding it (-1 when it hasn't written its pid yet), and release is nil then. A watcher
// keeps the lock until release or its exit.
func Arm(path string, pid int) (release func(), held int, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, 0, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); errors.Is(err, syscall.EWOULDBLOCK) {
		defer f.Close()
		return nil, pidIn(f), nil
	} else if err != nil {
		f.Close()
		return nil, 0, err
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, 0, err
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(pid)), 0); err != nil {
		f.Close()
		return nil, 0, err
	}
	return func() { f.Close() }, 0, nil
}

// Armed reports whether a watcher holds the lock at path, and its pid (-1 when it hasn't
// written it yet). No lock file is none armed.
func Armed(path string) (pid int, armed bool, err error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	} else if err != nil {
		return 0, false, err
	}
	defer f.Close()
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); {
	case errors.Is(err, syscall.EWOULDBLOCK):
		return pidIn(f), true, nil
	case err != nil:
		return 0, false, err
	}
	return 0, false, nil // closing f drops the lock it just took
}

// pidIn is the pid written in f, -1 when there is none yet.
func pidIn(f *os.File) int {
	b := make([]byte, 32)
	n, _ := f.ReadAt(b, 0)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b[:n])))
	if err != nil {
		return -1
	}
	return pid
}
