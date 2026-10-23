package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/jmmshn/bees/internal/harness/jsonfile"
	"github.com/jmmshn/bees/internal/port"
)

// TrustKey is the key of a folder's entry in Claude's config (projects[<dir>]) that says
// it starts there without its folder-trust prompt.
const TrustKey = "hasTrustDialogAccepted"

// lockWait bounds the wait for Claude's, or another bs's, edit of its config (motif 15);
// lockStale is when a lock nobody refreshed is dead, as Claude's lock library reads it.
const (
	lockWait  = 10 * time.Second
	lockStale = 10 * time.Second
)

var _ port.Truster = Adapter{}

// Trust marks dir trusted in Claude's config, so a worker in a [workspace.command]
// folder (new each spawn) doesn't wait at the trust prompt (bees-13.86). A dir in a git
// checkout is left alone: Claude trusts it through its repo, which is the keeper's to
// trust (R29).
func (a Adapter) Trust(dir string) error {
	if inGit(dir) {
		return nil
	}
	return a.editProjects(func(ps map[string]json.RawMessage) (bool, error) {
		p := map[string]json.RawMessage{}
		if raw, ok := ps[dir]; ok {
			if err := json.Unmarshal(raw, &p); err != nil {
				return false, fmt.Errorf("projects[%s] isn't an object", dir)
			}
		}
		if string(p[TrustKey]) == "true" {
			return false, nil
		}
		p[TrustKey] = json.RawMessage("true")
		raw, err := marshal(p)
		ps[dir] = raw
		return true, err
	})
}

// Untrust drops dir's entry, once its folder is gone, when it says dir is trusted: the
// entry Trust made, or one the keeper's answer made in a folder bs since removed. A git
// worktree has no entry of its own (Claude keys it by its repo), so it is untouched.
func (a Adapter) Untrust(dir string) error {
	return a.editProjects(func(ps map[string]json.RawMessage) (bool, error) {
		var p map[string]json.RawMessage
		if json.Unmarshal(ps[dir], &p) != nil || string(p[TrustKey]) != "true" {
			return false, nil
		}
		delete(ps, dir)
		return true, nil
	})
}

// inGit reports whether dir, or a folder above it, holds a .git.
func inGit(dir string) bool {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

// ConfigPath is Claude's config: $CLAUDE_CONFIG_DIR/.claude.json, else <Home>/.claude.json.
func (a Adapter) ConfigPath() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json"), nil
	}
	home := a.Home
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", fmt.Errorf("claude's config: %w", err)
		}
	}
	return filepath.Join(home, ".claude.json"), nil
}

// editProjects rewrites Claude's config's projects as edit leaves them (false: unchanged,
// nothing written), keeping every other key as it was. It holds Claude's own lock on the
// file, the one every Claude session takes to save it; a file changed since it was read
// anyway is read again. The write is a rename. An unparsable file is refused, never
// replaced.
func (a Adapter) editProjects(edit func(map[string]json.RawMessage) (bool, error)) error {
	path, err := a.ConfigPath()
	if err != nil {
		return err
	}
	unlock, err := lockDir(path+".lock", lockWait)
	if err != nil {
		return err
	}
	defer unlock()
	for range 3 {
		old, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("claude's config: %w", err)
		}
		keys := map[string]json.RawMessage{}
		if len(bytes.TrimSpace(old)) > 0 {
			if err := json.Unmarshal(old, &keys); err != nil {
				return fmt.Errorf("claude's config %s can't be read as JSON, so bs leaves it as it is: %w", path, err)
			}
		}
		ps := map[string]json.RawMessage{}
		if raw, ok := keys["projects"]; ok {
			if err := json.Unmarshal(raw, &ps); err != nil {
				return fmt.Errorf("claude's config %s: projects isn't an object, so bs leaves it as it is: %w", path, err)
			}
		}
		switch changed, err := edit(ps); {
		case err != nil:
			return fmt.Errorf("claude's config %s: %w, so bs leaves it as it is", path, err)
		case !changed:
			return nil
		}
		if keys["projects"], err = marshal(ps); err != nil {
			return err
		}
		b, err := marshal(keys)
		if err != nil {
			return err
		}
		var out bytes.Buffer
		if err := json.Indent(&out, b, "", "  "); err != nil {
			return err
		}
		switch err := jsonfile.Replace(path, old, out.Bytes()); {
		case errors.Is(err, jsonfile.ErrChanged):
			continue
		case err != nil:
			return fmt.Errorf("claude's config %s: %w", path, err)
		}
		return nil
	}
	return fmt.Errorf("claude's config %s: it kept changing while bs edited projects; try again", path)
}

// marshal is json.Marshal without escaping <, > and &, so values come back as Claude wrote
// them.
func marshal(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// lockDir takes the lock Claude takes on its config: the directory path, made by mkdir,
// whose holder touches it while it holds it. One untouched for lockStale is dead and is
// taken over, as Claude does. It waits up to wait.
func lockDir(path string, wait time.Duration) (unlock func(), err error) {
	for deadline := time.Now().Add(wait); ; {
		err := os.Mkdir(path, 0o700)
		if err == nil {
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("claude's config lock %s: %w", path, err)
		}
		if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) > lockStale {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("claude's config lock %s: held for %s, by Claude or another bs", path, wait)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
