package ipc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCheckSunPath(t *testing.T) {
	if err := checkSunPath("/"+strings.Repeat("a", darwinSunPathLen-2), darwinSunPathLen); err != nil {
		t.Fatalf("103-byte path refused: %v", err)
	}
	err := checkSunPath("/"+strings.Repeat("a", darwinSunPathLen-1), darwinSunPathLen)
	if !errors.Is(err, syscall.ENAMETOOLONG) {
		t.Fatalf("104-byte path: %v, want ENAMETOOLONG", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "104 bytes long") || !strings.Contains(msg, "at most 103 bytes") ||
		!strings.Contains(msg, EnvEndpoint) {
		t.Fatalf("unclear error: %q", msg)
	}
	if err := checkSunPath("/tmp/a\x00b", darwinSunPathLen); err == nil {
		t.Fatal("a NUL byte was accepted")
	}
}

func TestDarwinEndpoint(t *testing.T) {
	const tmp = "/var/folders/zz/zyxvpxvq6csfxvn_n0000000000000/T/" // as launchd sets TMPDIR
	cases := []struct {
		name, tmp, home, want string
	}{
		{"temporary directory", tmp, "/Users/ann", "/var/folders/zz/zyxvpxvq6csfxvn_n0000000000000/T/downex-501/rpc.sock"},
		{"without trailing slash", "/private/var/folders/x/T", "/Users/ann", "/private/var/folders/x/T/downex-501/rpc.sock"},
		{"no temporary directory", "", "/Users/ann", "/Users/ann/Library/Caches/Downex/run/rpc.sock"},
		{"relative temporary directory", "tmp", "/Users/ann/", "/Users/ann/Library/Caches/Downex/run/rpc.sock"},
		{"temporary directory too long", "/" + strings.Repeat("t", 90), "/Users/ann", "/Users/ann/Library/Caches/Downex/run/rpc.sock"},
		// "/" + 82 bytes + "/downex-501/rpc.sock" (20 bytes) is exactly 103 bytes.
		{"longest that fits", "/" + strings.Repeat("t", 82), "/Users/ann", "/" + strings.Repeat("t", 82) + "/downex-501/rpc.sock"},
		{"one byte too long", "/" + strings.Repeat("t", 83), "/Users/ann", "/Users/ann/Library/Caches/Downex/run/rpc.sock"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := darwinEndpoint(c.tmp, c.home, 501)
			if err != nil || got != c.want {
				t.Fatalf("darwinEndpoint(%q, %q) = %q, %v; want %q", c.tmp, c.home, got, err, c.want)
			}
			if len(got) >= darwinSunPathLen {
				t.Fatalf("%q does not fit sun_path", got)
			}
		})
	}
	// Neither fits, or neither is known: a clear error.
	if got, err := darwinEndpoint("/"+strings.Repeat("t", 90), "/"+strings.Repeat("h", 90), 501); !errors.Is(err, syscall.ENAMETOOLONG) {
		t.Fatalf("darwinEndpoint(long, long) = %q, %v; want ENAMETOOLONG", got, err)
	}
	if got, err := darwinEndpoint("", "", 501); err == nil || !strings.Contains(err.Error(), EnvEndpoint) {
		t.Fatalf("darwinEndpoint(\"\", \"\") = %q, %v; want an error naming %s", got, err, EnvEndpoint)
	}
}

// TestRefreshTimes: the times of every listed file are renewed until done is closed; a
// missing file is skipped.
func TestRefreshTimes(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	old := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		refreshTimes([]string{filepath.Join(dir, "missing"), a, b}, 5*time.Millisecond, done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		fa, erra := os.Stat(a)
		fb, errb := os.Stat(b)
		if erra == nil && errb == nil && fa.ModTime().After(old) && fb.ModTime().After(old) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("times not renewed: %v %v %v %v", fa, erra, fb, errb)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("refreshTimes did not return after done was closed")
	}
	if _, err := os.Stat(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refreshTimes created a missing file (%v)", err)
	}
}
