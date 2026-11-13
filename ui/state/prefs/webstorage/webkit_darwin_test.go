//go:build darwin

package webstorage

import (
	"bytes"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// wailsOriginFile is the origin file WebKit wrote beside today's progress windows' storage
// (wails://wails as top and frame origin, no ports).
var wailsOriginFile, _ = hex.DecodeString("05000000017761696c730500000001" + "7761696c7300" + "0500000001" + "7761696c73" + "0500000001" + "7761696c7300")

func utf16hex(s string) string {
	var b bytes.Buffer
	for _, u := range utf16.Encode([]rune(s)) {
		b.WriteByte(byte(u))
		b.WriteByte(byte(u >> 8))
	}
	return hex.EncodeToString(b.Bytes())
}

// makeDB writes a WebKit localStorage database at path holding vals, in WAL mode with the
// rows left in the log (as WebKit leaves them while it runs).
func makeDB(t *testing.T, path string, vals map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var sql strings.Builder
	sql.WriteString("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB NOT NULL ON CONFLICT FAIL);")
	for k, v := range vals {
		sql.WriteString("INSERT INTO ItemTable VALUES ('" + k + "', X'" + utf16hex(v) + "');")
	}
	cmd := exec.Command(sqlite3Path, "-cmd", ".dbconfig no_ckpt_on_close on", path, sql.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v %s", err, out)
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			m[p] = info.ModTime().String() + hex.EncodeToString(b)
		}
		return nil
	})
	return m
}

// Today's Mac windows' preferences come over from WebKit's storage: the newer layout and
// the legacy one, the main window's before the progress windows', other origins and keys
// left out, and nothing in WebKit's folders changed.
func TestReadWebKit(t *testing.T) {
	if _, err := os.Stat(sqlite3Path); err != nil {
		t.Skip("no sqlite3")
	}
	base := t.TempDir()
	keys := []string{"downex.density", "downex.columnWidths", "downex.progress.details", "downex.existingFile"}

	// The progress windows: the newer layout.
	pdir := filepath.Join(base, "com.downex.progress", "WebsiteData", "Default", "salt1", "hash1")
	makeDB(t, filepath.Join(pdir, "LocalStorage", "localstorage.sqlite3"), map[string]string{
		"downex.progress.details": "1", "downex.density": "compact",
	})
	if err := os.WriteFile(filepath.Join(pdir, "origin"), wailsOriginFile, 0o600); err != nil {
		t.Fatal(err)
	}
	// Another origin's storage in the same app: never read.
	odir := filepath.Join(base, "com.downex.progress", "WebsiteData", "Default", "salt1", "hash2")
	makeDB(t, filepath.Join(odir, "LocalStorage", "localstorage.sqlite3"), map[string]string{"downex.existingFile": "replace"})
	other := bytes.ReplaceAll(wailsOriginFile, []byte("wails"), []byte("other"))
	os.WriteFile(filepath.Join(odir, "origin"), other, 0o600)
	// The main window: the legacy layout.
	makeDB(t, filepath.Join(base, "com.downex.app", "WebsiteData", "LocalStorage", "wails_wails_0.localstorage"), map[string]string{
		"downex.density": "comfortable", "downex.columnWidths": `{"name":320}`, "unrelated": "x",
	})
	before := snapshot(t, base)

	got, err := ReadWebKit(base, WebKitBundles, append(keys, "bad'key"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"downex.density": "comfortable", "downex.columnWidths": `{"name":320}`, "downex.progress.details": "1"}
	if len(got) != len(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	after := snapshot(t, base)
	if len(after) != len(before) {
		t.Errorf("files changed: %d before, %d after", len(before), len(after))
	}
	for p, s := range before {
		if after[p] != s {
			t.Errorf("%s changed", p)
		}
	}

	if _, err := ReadWebKit(t.TempDir(), WebKitBundles, keys); err != ErrNoStorage {
		t.Errorf("an empty folder: %v, want ErrNoStorage", err)
	}
}

func TestWailsOrigin(t *testing.T) {
	if !wailsOrigin(wailsOriginFile) {
		t.Error("today's origin file is not read as wails://wails")
	}
	for _, b := range [][]byte{nil, wailsOriginFile[:10], bytes.ReplaceAll(wailsOriginFile, []byte("wails"), []byte("wailz"))} {
		if wailsOrigin(b) {
			t.Errorf("%x read as wails://wails", b)
		}
	}
}
