package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestNumericVersion(t *testing.T) {
	for in, want := range map[string]string{
		"0.9.2":     "0.9.2",
		"1.2.3-rc1": "1.2.3",
		"1.2.3.4":   "1.2.3",
		"2":         "2.0.0",
		"1.5-beta":  "1.5.0",
		"007.08.9":  "7.8.9",
		"dev":       "0.0.0",
		"v1.2.3":    "0.0.0",
		"70000.1.2": "65535.1.2",
		"1.1234567": "1.65535.0",
	} {
		if got := numericVersion(in); got != want {
			t.Errorf("numericVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func utf16le(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

// TestRunVersionText is scripts/build-windows.ps1 without -Version: "dev" builds, the numeric
// fields are 0.0.0, and the string fields keep "dev"; a release version is unchanged.
func TestRunVersionText(t *testing.T) {
	repo := filepath.Join("..", "..", "..")
	manifest := filepath.Join(repo, "packaging", "windows", "downex-ui.exe.manifest")
	icon := filepath.Join(repo, "packaging", "windows", "icon.ico")
	for _, tc := range []struct{ ver, num string }{{"dev", "0.0.0"}, {"0.9.2", "0.9.2"}, {"1.0.0-rc1", "1.0.0"}} {
		out := filepath.Join(t.TempDir(), "rsrc.syso")
		if err := run(out, manifest, icon, tc.ver, "Downex.exe", "Downex"); err != nil {
			t.Fatalf("run(%q): %v", tc.ver, err)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if want := `version="` + tc.num + `.0"`; !bytes.Contains(b, []byte(want)) {
			t.Errorf("%q: manifest lacks %s", tc.ver, want)
		}
		if !bytes.Contains(b, append(utf16le(tc.ver), 0, 0)) {
			t.Errorf("%q: the version strings do not keep the text", tc.ver)
		}
		// No string table (the neutral one SetFileVersion makes included) shows the number
		// in place of the text.
		if tc.num != tc.ver && bytes.Contains(b, append(utf16le(tc.num), 0, 0)) {
			t.Errorf("%q: a version string is the number %s, not the text", tc.ver, tc.num)
		}
	}
	for _, bad := range []string{"", "-x", "1.0 beta", "a/b"} {
		err := run(filepath.Join(t.TempDir(), "rsrc.syso"), manifest, icon, bad, "Downex.exe", "Downex")
		if err == nil || !strings.Contains(err.Error(), "version") {
			t.Errorf("run(%q) = %v, want a version error", bad, err)
		}
	}
}
