//go:build windows

package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthenticodeUnsigned(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string][]byte{"plain.txt": []byte("hello"), "fake.exe": []byte("MZ not really a program")} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := authenticode(p); !errors.Is(err, ErrUnsigned) {
			t.Errorf("%s: %v, want ErrUnsigned", name, err)
		}
		if s, err := VerifyAuthenticode(p); !errors.Is(err, ErrUnsigned) || s.Subject != "" {
			t.Errorf("VerifyAuthenticode(%s) = %+v, %v, want ErrUnsigned", name, s, err)
		}
	}
	// The test binary itself is not signed either.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := authenticode(exe); !errors.Is(err, ErrUnsigned) {
		t.Errorf("test binary: %v, want ErrUnsigned", err)
	}
	if err := authenticode(filepath.Join(dir, "missing.exe")); err == nil || errors.Is(err, ErrUnsigned) {
		t.Errorf("missing file: %v, want an error other than ErrUnsigned", err)
	}
}

// TestVerifyAuthenticodeSigner checks a program that carries an embedded Authenticode
// signature on every Windows 10/11 installation (most system files are catalog-signed,
// which WinVerifyTrust's file check does not cover).
func TestVerifyAuthenticodeSigner(t *testing.T) {
	var candidates []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
		if base := os.Getenv(env); base != "" {
			candidates = append(candidates, filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		candidates = append(candidates, filepath.Join(root, "System32", "MRT.exe"))
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		s, err := VerifyAuthenticode(p)
		if err != nil {
			t.Fatalf("VerifyAuthenticode(%s): %v", p, err)
		}
		if !strings.Contains(s.Subject, "Microsoft") {
			t.Errorf("VerifyAuthenticode(%s) subject %q, want a Microsoft signer", p, s.Subject)
		}
		if err := authenticode(p); err != nil {
			t.Errorf("authenticode(%s): %v", p, err)
		}
		return
	}
	t.Skip("no program with an embedded Authenticode signature found")
}
