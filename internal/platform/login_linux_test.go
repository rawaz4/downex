//go:build linux

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutostartEntry(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(cfg, "autostart", "downex.desktop")

	if err := SetLaunchAtLogin(false, ""); err != nil {
		t.Fatalf("disable without an entry: %v", err)
	}
	exe := "/opt/Down ex/$bin/down\\exd%"
	if err := SetLaunchAtLogin(true, exe, "--background"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `Exec="/opt/Down ex/\\$bin/down\\\\exd%%" "--background"` + "\n"
	if s := string(data); !strings.HasPrefix(s, "[Desktop Entry]\n") || !strings.Contains(s, want) ||
		!strings.Contains(s, "Type=Application\n") {
		t.Fatalf("entry =\n%s\nwant a line %q", s, want)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("entry mode = %v, %v", fi, err)
	}
	if err := SetLaunchAtLogin(true, exe, "--background"); err != nil {
		t.Fatalf("enable again: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(cfg, "autostart", ".downex-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	if err := SetLaunchAtLogin(true, "relative/downexd"); err == nil {
		t.Fatal("a relative executable was accepted")
	}
	if err := SetLaunchAtLogin(true, "/x/downexd", "a\nb"); err == nil {
		t.Fatal("a newline in an argument was accepted")
	}
	if err := SetLaunchAtLogin(false, ""); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("entry still exists: %v", err)
	}

	// Without XDG_CONFIG_HOME the entry is below ~/.config.
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	if err := SetLaunchAtLogin(true, "/x/downexd"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "autostart", "downex.desktop")); err != nil {
		t.Fatalf("entry below HOME: %v", err)
	}
}
