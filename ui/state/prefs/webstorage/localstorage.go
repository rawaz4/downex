package webstorage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// WailsOrigin is the origin today's window is served from (Wails v2.17 on Windows).
const WailsOrigin = "http://wails.localhost"

// DefaultDir is where today's window's localStorage is: %LOCALAPPDATA%\Downex\WebView2\
// EBWebView\Default\Local Storage\leveldb (ui/shell_windows.go webviewDataDir); "" when
// there is no such folder for this user.
func DefaultDir() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, "Downex", "WebView2", "EBWebView", "Default", "Local Storage", "leveldb")
}

// ErrNoStorage is a folder that holds no localStorage (missing, or not LevelDB).
var ErrNoStorage = errors.New("no localStorage here")

// ErrUnreadable is a folder the reader failed on in a way its own checks did not foresee.
var ErrUnreadable = errors.New("webstorage: unreadable folder")

// Read returns the values of keys that origin stored, read from the LevelDB folder dir
// without changing anything in it. A key that is not stored is left out. Chromium keeps
// each value under "_" + origin + "\x00" + key, the key and the value each starting with
// a byte that says how the rest is encoded: 0 UTF-16LE, 1 Latin-1.
//
// The reader checks every offset and length it takes from the files itself, and its fuzz
// targets run without the recover below. That recover is the last line of defence the
// package promises: a defect left in the reader reads as an unreadable folder (the
// caller's defaults) and never takes the window down.
func Read(dir, origin string, keys []string) (out map[string]string, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("%w: %v", ErrUnreadable, r)
		}
	}()
	d, err := readDB(dir)
	if err != nil {
		return nil, err
	}
	if len(d) == 0 {
		return nil, ErrNoStorage
	}
	out = map[string]string{}
	prefix := "_" + origin + "\x00"
	for k, rec := range d {
		if rec.deleted || !strings.HasPrefix(k, prefix) {
			continue
		}
		key, ok := decode([]byte(k[len(prefix):]))
		if !ok {
			continue
		}
		want := len(keys) == 0
		for _, x := range keys {
			want = want || x == key
		}
		if !want {
			continue
		}
		if value, ok := decode(rec.value); ok {
			out[key] = value
		}
	}
	return out, nil
}

// decode reads a Chromium localStorage string: a format byte, then UTF-16LE (0) or Latin-1
// (1) units.
func decode(b []byte) (string, bool) {
	if len(b) == 0 {
		return "", false
	}
	switch b[0] {
	case 0:
		b = b[1:]
		if len(b)%2 != 0 {
			return "", false
		}
		units := make([]uint16, len(b)/2)
		for i := range units {
			units[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
		}
		return string(utf16.Decode(units)), true
	case 1:
		r := make([]rune, len(b)-1)
		for i, c := range b[1:] {
			r[i] = rune(c)
		}
		return string(r), true
	}
	return "", false
}
