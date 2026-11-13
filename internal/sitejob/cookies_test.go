package sitejob

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// Each usable cookie of the header becomes one line for the page's own host, path "/",
// secure for https, a session cookie; unusable pairs are left out.
func TestCookieLines(t *testing.T) {
	got := cookieLines("https://WWW.YouTube.com:443/watch?v=x",
		"SID=abc; PREF=f6=4&tz=Europe.Berlin;  bad name=1; novalue; tab=a\tb; semi=; __Secure-3PSID=x-y_z")
	want := []string{
		"# Netscape HTTP Cookie File",
		"www.youtube.com\tFALSE\t/\tTRUE\t0\tSID\tabc",
		"www.youtube.com\tFALSE\t/\tTRUE\t0\tPREF\tf6=4&tz=Europe.Berlin",
		"www.youtube.com\tFALSE\t/\tTRUE\t0\tsemi\t",
		"www.youtube.com\tFALSE\t/\tTRUE\t0\t__Secure-3PSID\tx-y_z",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cookieLines\n got %q\nwant %q", got, want)
	}
	if got := cookieLines("http://127.0.0.1:8080/p", "a=1"); len(got) != 2 || got[1] != "127.0.0.1\tFALSE\t/\tFALSE\t0\ta\t1" {
		t.Errorf("http page: %q", got)
	}
	for _, header := range []string{"", ";;", "noequals", "a b=c", "x=\x7f"} {
		if got := cookieLines("https://example.com/", header); got != nil {
			t.Errorf("cookieLines(%q) = %q, want none", header, got)
		}
	}
}

// The cookie file is created in the run folder, private to the user, and not at all
// when no cookie is usable.
func TestWriteCookieFile(t *testing.T) {
	run := t.TempDir()
	path, err := writeCookieFile(run, "https://example.com/v", "a=1; b=2")
	if err != nil || path != filepath.Join(run, cookieFileName) {
		t.Fatalf("writeCookieFile = %q, %v", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(cookieLines("https://example.com/v", "a=1; b=2"), "\n") + "\n"; string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, %v; want 0600", fi.Mode().Perm(), err)
		}
	}
	if _, err := writeCookieFile(run, "https://example.com/v", "a=1"); err == nil {
		t.Error("an existing cookie file was overwritten")
	}
	empty := t.TempDir()
	if path, err := writeCookieFile(empty, "https://example.com/v", "not a cookie"); path != "" || err != nil {
		t.Errorf("unusable header: %q, %v", path, err)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Errorf("unusable header left %d files", len(entries))
	}
}
