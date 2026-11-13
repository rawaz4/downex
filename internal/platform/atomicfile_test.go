package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entry")
	if err := writeFileAtomic(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := writeFileAtomic(path, []byte("second"), 0o644); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "second" {
		t.Fatalf("contents = %q, %v; want \"second\"", data, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".downex-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	// A missing directory fails and leaves nothing behind.
	if err := writeFileAtomic(filepath.Join(dir, "missing", "entry"), []byte("x"), 0o644); err == nil {
		t.Fatal("writeFileAtomic into a missing directory succeeded")
	}
	// A directory in the way fails the rename; the temporary file is removed.
	blocked := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(filepath.Join(blocked, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(blocked, []byte("x"), 0o644); err == nil {
		t.Fatal("writeFileAtomic replaced a non-empty directory")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".downex-*")); len(left) != 0 {
		t.Fatalf("temporary files left after a failed rename: %v", left)
	}
}
