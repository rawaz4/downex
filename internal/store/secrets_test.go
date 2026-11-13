package store

import (
	"bytes"
	"path/filepath"
	"testing"
)

// TestSQLiteSecretBlobs: blobs are stored, replaced, read back after a reopen and
// removed; missing ones are reported as absent.
func TestSQLiteSecretBlobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "downex.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetBlob("site_login:a"); ok || err != nil {
		t.Fatalf("missing blob: ok %v, err %v", ok, err)
	}
	for _, b := range [][]byte{{1, 2, 3}, {0, 9, 8, 7}} {
		if err := s.PutBlob("site_login:a", b); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutBlob("proxy", []byte("p")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutBlob("empty", nil); err == nil {
		t.Error("an empty blob was stored")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = OpenSQLite(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if b, ok, err := s.GetBlob("site_login:a"); !ok || err != nil || !bytes.Equal(b, []byte{0, 9, 8, 7}) {
		t.Fatalf("blob after reopen = %v, %v, %v", b, ok, err)
	}
	if err := s.DeleteBlob("site_login:a"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBlob("never-stored"); err != nil {
		t.Fatalf("deleting a missing blob: %v", err)
	}
	if _, ok, _ := s.GetBlob("site_login:a"); ok {
		t.Fatal("blob still there after DeleteBlob")
	}
	if _, ok, _ := s.GetBlob("proxy"); !ok {
		t.Fatal("another blob went away")
	}
}
