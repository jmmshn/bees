package agy

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Mode is the agent mode bs launches agy in. accept-edits approves workspace file edits
// and needs no sandbox; auto needs agy's terminal sandbox, which a host whose AppArmor
// restricts unprivileged user namespaces refuses, and agy then runs in default mode,
// asking about every edit and command, saying so only in its log (bees-13.13.14).
const Mode = "accept-edits"

// Took is what one agy start's log says of its mode.
type Took struct {
	Log      string // the log file
	Asked    string // the mode it asked for, "" when the log names none
	Fallback string // agy's line saying the mode didn't take, "" when nothing says so
}

// agy's log lines (probed on Probed, bees-13.13.14): an interactive start says
// "--mode: overriding agent mode to auto" and, when auto can't run, "auto mode is
// unavailable because the terminal sandbox is unavailable, falling back to default"; print
// mode says "applying agent mode accept-edits", or "--mode auto is unavailable (terminal
// sandbox unavailable), continuing in the default mode".
var (
	askedLine    = regexp.MustCompile(`(?:overriding agent mode to|applying agent mode) (\S+)`)
	fallbackLine = regexp.MustCompile(`\bmode\b.*\bunavailable\b`)
	workspaceDir = regexp.MustCompile(`workspaceDirs=\[([^\]]*)\]`)
)

// LogDir is where agy writes its logs: log/ beside its settings.json.
func (a Adapter) LogDir() (string, error) {
	s, err := a.SettingsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(s), "log"), nil
}

// Took reads the log of the newest agy start that session name's launch made, its
// per-start dir <name>-dir-* among its workspace dirs; name "" takes any bs session's.
// ok is false when no log has one.
func (a Adapter) Took(name string) (t Took, ok bool, err error) {
	dir, err := a.LogDir()
	if err != nil {
		return t, false, err
	}
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return t, false, nil
	} else if err != nil {
		return t, false, fmt.Errorf("agy's logs: %w", err)
	}
	// cli-<yyyymmdd>_<hhmmss>.log: newest last by name.
	for _, e := range slices.Backward(ents) {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		t, ok, err := readLog(filepath.Join(dir, e.Name()), name)
		if err != nil || ok {
			return t, ok, err
		}
	}
	return t, false, nil
}

// readLog reads one agy log: ok when a workspace dir in it is a per-start dir of session
// name, or of any session for "".
func readLog(path, name string) (t Took, ok bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return t, false, fmt.Errorf("agy's log: %w", err)
	}
	defer f.Close()
	t.Log = path
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := workspaceDir.FindStringSubmatch(line); m != nil && !ok {
			ok = slices.ContainsFunc(strings.Fields(m[1]), func(d string) bool { return startDir(filepath.Base(d), name) })
		}
		if m := askedLine.FindStringSubmatch(line); m != nil && t.Asked == "" {
			t.Asked = m[1]
		}
		if t.Fallback == "" && fallbackLine.MatchString(line) {
			t.Fallback = strings.TrimSpace(line)
		}
	}
	if err := sc.Err(); err != nil {
		return t, false, fmt.Errorf("agy's log %s: %w", path, err)
	}
	return t, ok, nil
}

// startDir reports whether base is a per-start dir of session name, <name>-dir-<digits>
// (any name for ""), the dir Owns knows a launch by.
func startDir(base, name string) bool {
	i := strings.LastIndex(base, "-dir-")
	if i < 0 || name != "" && base[:i] != name {
		return false
	}
	n := base[i+len("-dir-"):]
	return n != "" && strings.Trim(n, "0123456789") == ""
}
