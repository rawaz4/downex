package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setConfigHome points os.UserConfigDir at dir (or makes it fail for "").
func setConfigHome(t *testing.T, dir string) {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("AppData", dir)
	case "darwin", "ios":
		t.Setenv("HOME", dir)
	case "plan9":
		t.Skip("plan9 config dir is not covered")
	default:
		t.Setenv("XDG_CONFIG_HOME", dir)
		if dir == "" {
			t.Setenv("HOME", "")
		}
	}
}

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	setConfigHome(t, home)
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(base, "Downex", "downex.db"); got != want {
		t.Fatalf("DefaultPath = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, home) {
		t.Fatalf("DefaultPath = %q, want it under %q", got, home)
	}
	info, err := os.Stat(filepath.Dir(got))
	if err != nil || !info.IsDir() {
		t.Fatalf("DefaultPath did not create %s: %v", filepath.Dir(got), err)
	}

	// Idempotent, and the result is usable by OpenSQLite.
	again, err := DefaultPath()
	if err != nil || again != got {
		t.Fatalf("second DefaultPath = %q, %v; want %q", again, err, got)
	}
	s, err := OpenSQLite(got)
	if err != nil {
		t.Fatalf("OpenSQLite(DefaultPath): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestDefaultPathWithoutConfigDir(t *testing.T) {
	setConfigHome(t, "")
	if p, err := DefaultPath(); err == nil {
		t.Fatalf("DefaultPath = %q with no config directory, want an error", p)
	}
}
