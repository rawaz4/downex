//go:build windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The tests write a uniquely named Run value (never "Downex") and remove it again.
func testRunValueName(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("DownexTest-%d-%d", os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() {
		if err := setRunValue(name, false, ""); err != nil {
			t.Errorf("remove test Run value %s: %v", name, err)
		}
	})
	return name
}

func TestRunValueSetVerifyRemove(t *testing.T) {
	name := testRunValueName(t)
	exe := filepath.Join(t.TempDir(), "dir with space", "downexd.exe")

	if _, ok, err := runValue(name); err != nil || ok {
		t.Fatalf("Run value before the test = %v, %v", ok, err)
	}
	// Removing a missing value succeeds.
	if err := setRunValue(name, false, ""); err != nil {
		t.Fatalf("remove missing value: %v", err)
	}
	if err := setRunValue(name, true, exe, "--background"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	want := `"` + exe + `" --background`
	if got, ok, err := runValue(name); err != nil || !ok || got != want {
		t.Fatalf("Run value = %q, %v, %v; want %q", got, ok, err, want)
	}
	// Idempotent, and a changed command line replaces the value.
	if err := setRunValue(name, true, exe, "--background"); err != nil {
		t.Fatalf("enable again: %v", err)
	}
	other := filepath.Join(t.TempDir(), "downexd.exe")
	if err := setRunValue(name, true, other, "--background", "a b"); err != nil {
		t.Fatalf("enable with another path: %v", err)
	}
	want = `"` + other + `" --background "a b"`
	if got, _, _ := runValue(name); got != want {
		t.Fatalf("Run value = %q, want %q", got, want)
	}
	if err := setRunValue(name, false, ""); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, ok, err := runValue(name); err != nil || ok {
		t.Fatalf("Run value after disable = %v, %v", ok, err)
	}
}

func TestRunValueRefusesBadCommands(t *testing.T) {
	name := testRunValueName(t)
	for _, c := range []struct {
		exe  string
		args []string
	}{
		{`downexd.exe`, nil},                     // relative
		{`C:\x\"downexd.exe`, nil},               // a quote would end the path
		{`C:\x\downexd.exe`, []string{"a\r\nb"}}, // control characters
		{"C:\\x\\down\x00exd.exe", nil},          // NUL
	} {
		if err := setRunValue(name, true, c.exe, c.args...); err == nil {
			t.Errorf("setRunValue(%q, %q) succeeded", c.exe, c.args)
		}
	}
	if _, ok, _ := runValue(name); ok {
		t.Fatal("a refused command line was written")
	}
}
