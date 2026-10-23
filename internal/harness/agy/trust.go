package agy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/jmmshn/bees/internal/harness/jsonfile"
	"github.com/jmmshn/bees/internal/port"
)

// Trusted is the key of agy's settings that lists the folders it starts in without its
// trust prompt, each an exact path: a parent doesn't cover its children (bees-13.80 probe 2).
const Trusted = "trustedWorkspaces"

// lockWait bounds the wait for another bs's edit of the settings (motif 15).
const lockWait = 10 * time.Second

var _ port.Truster = Adapter{}

// Trust adds dir to agy's trusted folders (keeper decision 2026-10-23: only the path bs
// just created, only that key).
func (a Adapter) Trust(dir string) error {
	return a.editTrusted(func(ds []string) []string {
		if slices.Contains(ds, dir) {
			return nil
		}
		return append(ds, dir)
	})
}

// Untrust removes dir, exactly, from agy's trusted folders.
func (a Adapter) Untrust(dir string) error {
	return a.editTrusted(func(ds []string) []string {
		if !slices.Contains(ds, dir) {
			return nil
		}
		return slices.DeleteFunc(ds, func(d string) bool { return d == dir })
	})
}

// SettingsPath is agy's settings.json: Settings, else ~/.gemini/antigravity-cli/settings.json.
func (a Adapter) SettingsPath() (string, error) {
	if a.Settings != "" {
		return a.Settings, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("agy's settings: %w", err)
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), nil
}

// editTrusted rewrites the trusted folders as edit returns them (nil: unchanged, nothing
// written), keeping every other key as it was. Other bs processes wait on a lock beside
// the file; agy rewrites it without one, so a file changed since it was read is read
// again. The write is a rename: agy never reads half a file. An unparsable file is
// refused, never replaced.
func (a Adapter) editTrusted(edit func([]string) []string) error {
	path, err := a.SettingsPath()
	if err != nil {
		return err
	}
	unlock, err := lockFile(path+".lock", lockWait)
	if err != nil {
		return err
	}
	defer unlock()
	for range 3 {
		old, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("agy's settings: %w", err)
		}
		keys := map[string]json.RawMessage{}
		if len(bytes.TrimSpace(old)) > 0 {
			if err := json.Unmarshal(old, &keys); err != nil {
				return fmt.Errorf("agy's settings %s can't be read as JSON, so bs leaves it as it is: %w", path, err)
			}
		}
		var ds []string
		if raw, ok := keys[Trusted]; ok {
			if err := json.Unmarshal(raw, &ds); err != nil {
				return fmt.Errorf("agy's settings %s: %s isn't a list of paths, so bs leaves it as it is: %w", path, Trusted, err)
			}
		}
		ds = edit(slices.Clone(ds))
		if ds == nil {
			return nil
		}
		if keys[Trusted], err = json.Marshal(ds); err != nil {
			return err
		}
		b, err := json.MarshalIndent(keys, "", "  ")
		if err != nil {
			return err
		}
		switch err := jsonfile.Replace(path, old, append(b, '\n')); {
		case errors.Is(err, jsonfile.ErrChanged):
			continue
		case err != nil:
			return fmt.Errorf("agy's settings %s: %w", path, err)
		}
		return nil
	}
	return fmt.Errorf("agy's settings %s: it kept changing while bs edited %s; try again", path, Trusted)
}

// lockFile takes an exclusive flock(2) on path, made if missing, waiting up to wait.
func lockFile(path string, wait time.Duration) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("agy's settings lock: %w", err)
	}
	for deadline := time.Now().Add(wait); ; {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		case !errors.Is(err, syscall.EWOULDBLOCK):
			f.Close()
			return nil, fmt.Errorf("agy's settings lock %s: %w", path, err)
		case time.Now().After(deadline):
			f.Close()
			return nil, fmt.Errorf("agy's settings lock %s: another bs held it for %s", path, wait)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
