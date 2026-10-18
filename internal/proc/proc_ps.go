//go:build !linux

package proc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Parent returns pid's parent, or 0 if it can't be read.
func Parent(pid int) int {
	out, err := ps(pid, "ppid=")
	if err != nil {
		return 0
	}
	ppid, _ := strconv.Atoi(out)
	return ppid
}

// Start is the start time exactly as Claude records it on macOS:
// LC_ALL=C TZ=UTC ps -o lstart= -p <pid>, trimmed.
func Start(pid int) (string, error) {
	return ps(pid, "lstart=")
}

func ps(pid int, field string) (string, error) {
	cmd := exec.Command("ps", "-o", field, "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if s := strings.TrimSpace(string(out)); err == nil && s != "" {
		return s, nil
	}
	return "", fmt.Errorf("pid %d: no such process", pid)
}
