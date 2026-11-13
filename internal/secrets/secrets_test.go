package secrets

import (
	"bytes"
	"errors"
	"testing"
)

func testKeyring(t *testing.T) (Keyring, func(kind, id string) bool) {
	t.Helper()
	k, err := NewFileKeyring(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileKeyring: %v", err)
	}
	has := func(kind, id string) bool {
		name := kind
		if id != "" {
			name = kind + ":" + id
		}
		_, err := k.Get(name)
		return err == nil
	}
	return k, has
}

func TestFileKeyringRoundTrip(t *testing.T) {
	k, _ := testKeyring(t)
	// Test-scoped names only.
	const name = "test-proxy"
	secret := []byte("hunter2-\x00-binary")
	if err := k.Set(name, secret); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := k.Get(name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("Get = %q, want %q", got, secret)
	}
	if err := k.Delete(name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := k.Get(name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after Delete, Get err = %v, want ErrNotFound", err)
	}
	// Deleting a missing name is not an error.
	if err := k.Delete(name); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}

func TestFileKeyringReplaces(t *testing.T) {
	k, _ := testKeyring(t)
	const name = "test-site:abc"
	if err := k.Set(name, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := k.Set(name, []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, _ := k.Get(name)
	if string(got) != "second" {
		t.Fatalf("Get = %q, want second", got)
	}
}

func TestInvalidNamesRejected(t *testing.T) {
	k, _ := testKeyring(t)
	for _, bad := range []string{"", "../escape", "has space", "a/b", "a\\b"} {
		if err := k.Set(bad, []byte("x")); err == nil {
			t.Errorf("Set(%q) accepted an invalid name", bad)
		}
	}
}

func TestEmptySecretRejected(t *testing.T) {
	k, _ := testKeyring(t)
	if err := k.Set("test-x", nil); err == nil {
		t.Error("empty secret should be rejected")
	}
}

func TestFileBlobStore(t *testing.T) {
	bs, err := NewFileBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := bs.PutBlob("test-b", []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	b, ok, err := bs.GetBlob("test-b")
	if err != nil || !ok || !bytes.Equal(b, []byte{1, 2, 3}) {
		t.Fatalf("GetBlob = %v %v %v", b, ok, err)
	}
	if _, ok, _ := bs.GetBlob("test-missing"); ok {
		t.Error("missing blob reported present")
	}
	if err := bs.DeleteBlob("test-b"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := bs.GetBlob("test-b"); ok {
		t.Error("blob still present after delete")
	}
}
