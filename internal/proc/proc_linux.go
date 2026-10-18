package proc

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Parent returns pid's parent, or 0 if it can't be read.
func Parent(pid int) int {
	f, err := stat(pid)
	if err != nil {
		return 0
	}
	ppid, _ := strconv.Atoi(f[1])
	return ppid
}

// Start is field 22 of /proc/<pid>/stat, the start time in clock ticks.
func Start(pid int) (string, error) {
	f, err := stat(pid)
	if err != nil {
		return "", err
	}
	return f[19], nil
}

// stat returns the fields of /proc/<pid>/stat after the command name.
func stat(pid int) ([]string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil, err
	}
	// The command name is in parentheses and may contain spaces; fields resume after ')'.
	f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
	if len(f) < 20 {
		return nil, fmt.Errorf("pid %d: short /proc stat", pid)
	}
	return f, nil
}
