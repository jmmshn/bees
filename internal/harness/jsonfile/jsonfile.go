// Package jsonfile writes a harness's JSON file that its own program rewrites too, never
// leaving it half written or overwriting a change made since it was read.
package jsonfile

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrChanged is a file that no longer holds what was read: read it again.
var ErrChanged = errors.New("changed since it was read")

// Replace writes b to path by rename, with path's mode (0600 for a new file), unless path
// no longer holds old (ErrChanged).
func Replace(path string, old, b []byte) error {
	mode := fs.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".bs-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // gone already once renamed
	_, err = f.Write(b)
	err = errors.Join(err, f.Chmod(mode), f.Sync(), f.Close())
	if err != nil {
		return err
	}
	now, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if !bytes.Equal(now, old) {
		return ErrChanged
	}
	return os.Rename(f.Name(), path)
}
