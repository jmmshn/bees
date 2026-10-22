package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// bsx never opens the real logbooks before the swap: not through ~/.local/share/bees,
// $XDG_DATA_HOME/bees, or a symlink to either.
func TestLoadConfigRefusesTheRealLogbookDir(t *testing.T) {
	home, cfg, data := t.TempDir(), t.TempDir(), t.TempDir()
	real := filepath.Join(home, ".local", "share", "bees")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg, "bees", "sessions", "toy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home, "XDG_CONFIG_HOME=" + cfg, "XDG_DATA_HOME=" + data, "HERDR_SESSION=toy"}
	for _, d := range []string{real, link, filepath.Join(data, "bees")} {
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("project = \"toy\"\n[paths]\nlogbook_dir = \""+d+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(env); !errors.Is(err, ErrDefaultConfig) {
			t.Errorf("logbook_dir %s: %v, want ErrDefaultConfig", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("project = \"toy\"\n[paths]\nlogbook_dir = \""+t.TempDir()+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(env); err != nil {
		t.Fatalf("a toy's own logbook dir: %v", err)
	}
	if _, err := loadConfig([]string{"HOME=" + home}); !errors.Is(err, ErrDefaultConfig) {
		t.Fatalf("no HERDR_SESSION: %v", err)
	}
}
