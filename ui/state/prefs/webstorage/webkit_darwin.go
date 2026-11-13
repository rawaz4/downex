//go:build darwin

package webstorage

// [mac webkit] Today's Mac window is Wails' WKWebView: its localStorage is WebKit's, one
// SQLite file per origin (table ItemTable: key TEXT, value BLOB = the UTF-16LE string) under
// ~/Library/WebKit/<bundle id>/WebsiteData, either
//
//	Default/<salt hash>/<origin hash>/LocalStorage/localstorage.sqlite3, beside a file
//	"origin" that names the top and frame origins (macOS 14+), or
//	LocalStorage/<scheme>_<host>_<port>.localstorage (earlier releases).
//
// Wails serves the page as wails://wails. Nothing here is changed: each database is copied,
// with its write-ahead log, into a folder of its own and read there with the system's
// sqlite3 (/usr/bin/sqlite3, part of every macOS), so that no SQLite library is linked into
// the window.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// WebKitBundles are today's Mac windows' bundle ids: the main window's (Downex.app), then
// the progress windows' (Downex Progress.app). A key both stored is the main window's.
var WebKitBundles = []string{"com.downex.app", "com.downex.progress"}

// webKitScheme and webKitHost are the origin Wails serves today's page from on macOS.
const webKitScheme, webKitHost = "wails", "wails"

// sqlite3Path is the system's sqlite3 (a variable for tests).
var sqlite3Path = "/usr/bin/sqlite3"

// webKitReadTimeout bounds one database's read.
const webKitReadTimeout = 5 * time.Second

// WebKitDefaultDir is where WebKit keeps each app's website data: ~/Library/WebKit; "" when
// there is no home folder.
func WebKitDefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, "Library", "WebKit")
}

// ReadWebKit returns the values of keys that today's Mac windows stored in WebKit's
// localStorage under base (WebKitDefaultDir() layout), bundles in order of precedence. A key
// that is not stored is left out; ErrNoStorage when no database of the origin is there.
func ReadWebKit(base string, bundles, keys []string) (map[string]string, error) {
	safe := keys[:0:0]
	for _, k := range keys {
		if plainKey(k) {
			safe = append(safe, k)
		}
	}
	out := map[string]string{}
	found := false
	var errs []error
	for _, b := range bundles {
		for _, db := range webKitDatabases(filepath.Join(base, b, "WebsiteData")) {
			found = true
			vals, err := readWebKitDB(db, safe)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for k, v := range vals {
				if _, ok := out[k]; !ok {
					out[k] = v
				}
			}
		}
	}
	if !found {
		return nil, ErrNoStorage
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrUnreadable, errors.Join(errs...))
	}
	return out, nil
}

// webKitDatabases are the origin's databases in a WebsiteData folder: the newer layout's
// first, then the legacy one's.
func webKitDatabases(data string) []string {
	var dbs []string
	origins, _ := filepath.Glob(filepath.Join(data, "Default", "*", "*", "origin"))
	for _, o := range origins {
		b, err := readSmall(o, 4096)
		if err != nil || !wailsOrigin(b) {
			continue
		}
		db := filepath.Join(filepath.Dir(o), "LocalStorage", "localstorage.sqlite3")
		if st, err := os.Stat(db); err == nil && st.Mode().IsRegular() {
			dbs = append(dbs, db)
		}
	}
	legacy, _ := filepath.Glob(filepath.Join(data, "LocalStorage", webKitScheme+"_"+webKitHost+"_*.localstorage"))
	for _, db := range legacy {
		if st, err := os.Stat(db); err == nil && st.Mode().IsRegular() {
			dbs = append(dbs, db)
		}
	}
	return dbs
}

// wailsOrigin reports an origin file whose top and frame origins are both wails://wails.
// Each origin is its scheme and host (a uint32 length, a byte that is 1 for Latin-1 or 0
// for UTF-16LE, the characters) and a port (a byte 0, or 1 and a uint16).
func wailsOrigin(b []byte) bool {
	r := bytes.NewReader(b)
	for range 2 {
		scheme, ok1 := originString(r)
		host, ok2 := originString(r)
		if !ok1 || !ok2 || scheme != webKitScheme || host != webKitHost {
			return false
		}
		has, err := r.ReadByte()
		if err != nil {
			return false
		}
		if has != 0 {
			var port uint16
			if binary.Read(r, binary.LittleEndian, &port) != nil {
				return false
			}
		}
	}
	return true
}

func originString(r *bytes.Reader) (string, bool) {
	var n uint32
	if binary.Read(r, binary.LittleEndian, &n) != nil || n > 1024 {
		return "", false
	}
	latin1, err := r.ReadByte()
	if err != nil {
		return "", false
	}
	if latin1 != 0 {
		b := make([]byte, n)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", false
		}
		return string(b), true
	}
	b := make([]byte, 2*n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", false
	}
	return decodeUTF16(b), true
}

// readWebKitDB reads keys from a copy of the database at db (its log too).
func readWebKitDB(db string, keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	tmp, err := os.MkdirTemp("", "downex-webkit-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	copyDB := filepath.Join(tmp, "localstorage.sqlite3")
	if err := copySmall(db, copyDB); err != nil {
		return nil, err
	}
	if err := copySmall(db+"-wal", copyDB+"-wal"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = "'" + k + "'" // plainKey: no quote can be in k
	}
	q := "SELECT key, hex(value) FROM ItemTable WHERE key IN (" + strings.Join(quoted, ",") + ");"
	ctx, cancel := context.WithTimeout(context.Background(), webKitReadTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sqlite3Path, "-batch", "-noheader", "-list", "-separator", "\t", copyDB, q)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + tmp}
	cmd.Dir = tmp
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("webstorage: sqlite3: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(stdout), "\n") {
		k, h, ok := strings.Cut(line, "\t")
		if !ok || !want[k] {
			continue
		}
		raw, err := hex.DecodeString(strings.TrimSpace(h))
		if err != nil || len(raw)%2 != 0 {
			continue
		}
		out[k] = decodeUTF16(raw)
	}
	return out, nil
}

// plainKey: a key made of letters, digits, '.', '_' and '-' (every state key), safe to put
// in the query.
func plainKey(k string) bool {
	if k == "" || len(k) > 128 {
		return false
	}
	for _, c := range k {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

// maxWebKitFile bounds a database or log copied (today's hold a few keys).
const maxWebKitFile = 64 << 20

func readSmall(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// copySmall copies a regular file of at most maxWebKitFile bytes from src to dst.
func copySmall(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() > maxWebKitFile {
		return fmt.Errorf("webstorage: %s is not a database file", filepath.Base(src))
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(in, maxWebKitFile)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
