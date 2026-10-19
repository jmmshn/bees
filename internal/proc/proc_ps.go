//go:build !linux

package proc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
