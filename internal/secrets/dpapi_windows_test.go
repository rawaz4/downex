//go:build windows

package secrets

import (
	"bytes"
	"errors"
	"testing"
)

// TestDPAPIRoundTrip exercises the real DPAPI backend: a secret stored through the
// keyring is encrypted (the on-disk blob differs from the plaintext) and decrypts back.
func TestDPAPIRoundTrip(t *testing.T) {
	dir := t.TempDir()
	k, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const name = "test-dpapi"
	secret := []byte("correct horse battery staple")
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

	// The stored blob must be ciphertext, not the plaintext.
	bs, err := NewFileBlobStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	blob, ok, err := bs.GetBlob(name)
	if err != nil || !ok {
		t.Fatalf("blob missing: %v %v", ok, err)
	}
	if bytes.Contains(blob, secret) {
		t.Fatal("stored blob contains the plaintext secret")
	}

	if err := k.Delete(name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := k.Get(name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after Delete, Get err = %v, want ErrNotFound", err)
	}
}

// TestDPAPIProtectUnprotect checks the low-level primitives directly.
func TestDPAPIProtectUnprotect(t *testing.T) {
	plain := []byte("some-secret-bytes")
	blob, err := dpapiProtect(plain)
	if err != nil {
		t.Fatalf("dpapiProtect: %v", err)
	}
	if bytes.Equal(blob, plain) || len(blob) == 0 {
		t.Fatal("protected blob is not ciphertext")
	}
	back, err := dpapiUnprotect(blob)
	if err != nil {
		t.Fatalf("dpapiUnprotect: %v", err)
	}
	if !bytes.Equal(back, plain) {
		t.Fatalf("round trip = %q, want %q", back, plain)
	}
}
