package proc

import (
	"fmt"
	"os"
	"syscall"
)

// Private fails unless path, not followed through a symlink, is the user's and closed to
// group and others (ADR-0009 SEC-1, T3): a ring socket, its dir, a key file, herdr's
// socket. A missing path is os.ErrNotExist.
func Private(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to uid %d, not you", path, st.Uid)
	}
	if m := fi.Mode().Perm(); m&0o077 != 0 {
		return fmt.Errorf("%s is mode %04o: group or others can reach it (want 0600, 0700 for a dir)", path, m)
	}
	return nil
}
