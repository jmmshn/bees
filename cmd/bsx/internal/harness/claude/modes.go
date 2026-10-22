package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmmshn/bees/cmd/bsx/internal/proc"
)

// Modes checks the file modes Claude's cross-session ring rests on (ADR-0009 SEC-1, T3)
// in home's ~/.claude/sessions: the directory, and every socket's directory, 0700 or
// stricter and owned by the user; each socket and key file 0600 or stricter. A file gone
// since the directory was listed (a session exiting) is skipped. It returns what it
// checked, or the first mode that fails.
func (Adapter) Modes(home string) (string, error) {
	dir := filepath.Join(home, ".claude", "sessions")
	if err := proc.Private(dir); errors.Is(err, os.ErrNotExist) {
		return dir + " is missing: no Claude session has run yet", nil
	} else if err != nil {
		return "", err
	}
	es, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	sockets, keys := 0, 0
	for _, e := range es {
		path := filepath.Join(dir, e.Name())
		switch {
		case strings.HasSuffix(e.Name(), ".key"):
			switch err := proc.Private(path); {
			case err == nil:
				keys++
			case !errors.Is(err, os.ErrNotExist): // gone since the listing: a session exiting
				return "", err
			}
		case strings.HasSuffix(e.Name(), ".json"):
			b, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return "", err
			}
			var r struct{ MessagingSocketPath string }
			if json.Unmarshal(b, &r) != nil || r.MessagingSocketPath == "" {
				continue // no ring endpoint to check
			}
			for _, p := range []string{filepath.Dir(r.MessagingSocketPath), r.MessagingSocketPath} {
				if err := proc.Private(p); err != nil && !errors.Is(err, os.ErrNotExist) {
					return "", err
				}
			}
			sockets++
		}
	}
	return fmt.Sprintf("%s, %d ring sockets and their dirs, %d key files: none open to group or others", dir, sockets, keys), nil
}
