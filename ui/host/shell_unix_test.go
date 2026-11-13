//go:build !windows

package host

import (
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
)

// Open with… is Windows-only for now (Linux: with its window's desktop integration;
// macOS: MACOS-SPEC §5); elsewhere the page is told, and Startup says so (CanOpenWith).
func TestOpenWithIsUnimplemented(t *testing.T) {
	err := NewShell().OpenWith(filepath.Join(t.TempDir(), "a.txt"))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("OpenWith = %v, want unimplemented", err)
	}
}

func TestExecutableBitIsAProgram(t *testing.T) {
	dir := t.TempDir()
	for name, mode := range map[string]os.FileMode{"tool": 0o755, "data": 0o644} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, nil, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := RiskNone
		if mode&0o111 != 0 {
			want = RiskProgram
		}
		if got := RiskOf(p, info); got != want {
			t.Errorf("RiskOf(%s, %v) = %v, want %v", name, mode, got, want)
		}
	}
}
