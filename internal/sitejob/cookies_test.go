package sitejob

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Each usable cookie of the header becomes one line for the page's own host, path "/",
// secure for https, a session cookie; unusable pairs are left out.
func TestCookieLines(t *testing.T) {
	got := cookieLines("https://WWW.YouTube.com:443/watch?v=x",
		"SID=abc; PREF=f6=4&tz=Europe.Berlin;  bad name=1; novalue; tab=a\tb; semi=; __Secure-3PSID=x-y_z", "")
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
	if got := cookieLines("http://127.0.0.1:8080/p", "a=1", ""); len(got) != 2 || got[1] != "127.0.0.1\tFALSE\t/\tFALSE\t0\ta\t1" {
		t.Errorf("http page: %q", got)
	}
	for _, header := range []string{"", ";;", "noequals", "a b=c", "x=\x7f"} {
		if got := cookieLines("https://example.com/", header, ""); got != nil {
			t.Errorf("cookieLines(%q) = %q, want none", header, got)
		}
	}
}

// With the sign-in's domain (§6.3) every cookie is for that domain and its subdomains,
// so that the site's other hosts (an API host) get the sign-in too.
func TestCookieLinesDomain(t *testing.T) {
	got := cookieLines("https://www.instagram.example/reel/abc/", "sessionid=s1; csrftoken=c2", "instagram.example")
	want := []string{
		"# Netscape HTTP Cookie File",
		".instagram.example\tTRUE\t/\tTRUE\t0\tsessionid\ts1",
		".instagram.example\tTRUE\t/\tTRUE\t0\tcsrftoken\tc2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cookieLines\n got %q\nwant %q", got, want)
	}
}

// The cookie file is created in the run folder, private to the user, and not at all
// when no cookie is usable.
func TestWriteCookieFile(t *testing.T) {
	run := t.TempDir()
	path, n, err := writeCookieFile(run, "https://example.com/v", "a=1; b=2", "example.com")
	if err != nil || path != filepath.Join(run, cookieFileName) || n != 2 {
		t.Fatalf("writeCookieFile = %q, %d, %v", path, n, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(cookieLines("https://example.com/v", "a=1; b=2", "example.com"), "\n") + "\n"; string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
	checkPrivate(t, path)
	if _, _, err := writeCookieFile(run, "https://example.com/v", "a=1", ""); err == nil {
		t.Error("an existing cookie file was overwritten")
	}
	empty := t.TempDir()
	if path, n, err := writeCookieFile(empty, "https://example.com/v", "not a cookie", ""); path != "" || n != 0 || err != nil {
		t.Errorf("unusable header: %q, %d, %v", path, n, err)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Errorf("unusable header left %d files", len(entries))
	}
}
