//go:build darwin || linux

package filemark

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func getxattr(path, name string) ([]byte, error) {
	buf := make([]byte, 64<<10)
	n, err := unix.Lgetxattr(path, name, buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func TestMarkXattrs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.zip")
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := Origin{URL: "https://user:pw@dl.example.com/a.zip?sig=abc", Referrer: "https://example.com/page"}
	err := Mark(path, o)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
		t.Skipf("the temporary directory's file system has no user attributes: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	cleanURL, cleanRef := "https://dl.example.com/a.zip?sig=abc", "https://example.com/page"
	switch runtime.GOOS {
	case "darwin":
		q, err := getxattr(path, XattrQuarantine)
		if err != nil || !bytes.HasPrefix(q, []byte("0081;")) || !bytes.Contains(q, []byte(";Downex;")) {
			t.Fatalf("quarantine = %q, %v", q, err)
		}
		w, err := getxattr(path, XattrWhereFroms)
		if err != nil || !bytes.Equal(w, StringArrayPlist([]string{cleanURL, cleanRef})) {
			t.Fatalf("where froms = %x, %v", w, err)
		}
	case "linux":
		for name, want := range map[string]string{XattrOriginURL: cleanURL, XattrReferrerURL: cleanRef} {
			got, err := getxattr(path, name)
			if err != nil || string(got) != want {
				t.Fatalf("%s = %q, %v; want %q", name, got, err, want)
			}
		}
	}
}

func TestMarkRefusesSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.zip")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.zip")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Mark(link, Origin{URL: "https://example.com/"}); err == nil {
		t.Fatal("Mark of a symbolic link succeeded")
	}
}
