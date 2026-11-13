package validate

import "testing"

// webURLCases are `new URL(input)` as a WHATWG URL parser reads it (Node.js 24's, run on these
// inputs): whether it parses, its protocol, hostname and pathname. The checks read addresses
// through text.ParseURL; these cases hold them to what today's window gets.
var webURLCases = []struct {
	input        string
	ok           bool
	scheme, host string
	path         string
}{
	{"https://example.com/a", true, "https", "example.com", "/a"},
	{"http://127.0.0.1:8080/x?y=1", true, "http", "127.0.0.1", "/x"},
	{"ftp://example.com/a", true, "ftp", "example.com", "/a"},
	{"not a url", false, "", "", ""},
	{"http:example.com", true, "http", "example.com", "/"},
	{"http://exa mple.com/", false, "", "", ""},
	{"https://\u4f8b\u3048.jp/x", true, "https", "xn--r8jz45g.jp", "/x"},
	{"http://1.2.3.256/", false, "", "", ""},
	{"http://1.2.3.4.5/", false, "", "", ""},
	{"http://0x7f.1/", true, "http", "127.0.0.1", "/"},
	{"http://[::1]:8080/", true, "http", "[::1]", "/"},
	{"http://[::1/", false, "", "", ""},
	{"http://[1:2:3:4:5:6:7:8:9]/", false, "", "", ""},
	{"http://[::ffff:1.2.3.4]/", true, "http", "[::ffff:102:304]", "/"},
	{"http://[::ffff:1.2.3.04]/", false, "", "", ""},
	{"http://a:65536/", false, "", "", ""},
	{"http://a:65535/", true, "http", "a", "/"},
	{"http://a:/", true, "http", "a", "/"},
	{"http://@a/", true, "http", "a", "/"},
	{"http://u@/", false, "", "", ""},
	{"http://%41.com/", true, "http", "a.com", "/"},
	{"http://a%20b/", false, "", "", ""},
	{"http://xn--a.com/", false, "", "", ""},
	{"http://xn--nxasmq6b.com/", true, "http", "xn--nxasmq6b.com", "/"},
	{"mailto:x@y", true, "mailto", "", "x@y"},
	{"javascript:alert(1)", true, "javascript", "", "alert(1)"},
	{"C:\\Users\\x", true, "c", "", "\\Users\\x"},
	{"file:///C:/x", true, "file", "", "/C:/x"},
	{"file://server/share", true, "file", "server", "/share"},
	{"file://exa mple/x", false, "", "", ""},
	{"https:///x", true, "https", "x", "/"},
	{"http://\u2028a/", false, "", "", ""},
	{"http://a\u00adb.com/", true, "http", "ab.com", "/"},
	{"http://\uff25\uff38.com/", true, "http", "ex.com", "/"},
	{"http://example.com:80abc/", false, "", "", ""},
	{"  https://example.com  ", true, "https", "example.com", "/"},
	{"http://a..b/", true, "http", "a..b", "/"},
	{"http://.../", true, "http", "...", "/"},
	{"http://_x_.com/", true, "http", "_x_.com", "/"},
	{"HTTP://Example.COM/", true, "http", "example.com", "/"},
	{"wss://x/", true, "wss", "x", "/"},
	{"http://a\x09b.com/", true, "http", "ab.com", "/"},
	{"http://-a-.com/", true, "http", "-a-.com", "/"},
	{"http://a.b/c/../d.m3u8", true, "http", "a.b", "/d.m3u8"},
	{"https://cdn.example.com/v/Manifest.MPD", true, "https", "cdn.example.com", "/v/Manifest.MPD"},
	{"https://x/a.m3u8/..", true, "https", "x", "/"},
	{"https://x/a.m3u8/.", true, "https", "x", "/a.m3u8/"},
	{"https://x/a%2Em3u8", true, "https", "x", "/a%2Em3u8"},
	{"http:\\\\x\\a.mpd", true, "http", "x", "/a.mpd"},
	{"http://[::]/", true, "http", "[::]", "/"},
	{"http://[1::2::3]/", false, "", "", ""},
	{"http://[:1]/", false, "", "", ""},
	{"http://[1:]/", false, "", "", ""},
	{"http://[1:2:3:4:5:6:7::]/", true, "http", "[1:2:3:4:5:6:7:0]", "/"},
	{"http://[::1.2.3]/", false, "", "", ""},
	{"http://999999999/", true, "http", "59.154.201.255", "/"},
	{"http://4294967295/", true, "http", "255.255.255.255", "/"},
	{"http://4294967296/", false, "", "", ""},
	{"http://0x100000000/", false, "", "", ""},
	{"http://1.0x1000000/", false, "", "", ""},
	{"http://08/", false, "", "", ""},
	{"http://0.08/", false, "", "", ""},
	{"http://1.2.3.4./", true, "http", "1.2.3.4", "/"},
	{"http://foo.09/", false, "", "", ""},
	{"http://foo.0x/", false, "", "", ""},
	{"http://foo.0x1g/", true, "http", "foo.0x1g", "/"},
	{"http://x/a/%2e%2E/b.mpd", true, "http", "x", "/b.mpd"},
	{"file:", true, "file", "", "/"},
	{"file:C:/x", true, "file", "", "/C:/x"},
	{"foo://exa mple/", false, "", "", ""},
	{"foo://[x]/", false, "", "", ""},
	{"http://a<b/", false, "", "", ""},
	{"http://\ufffd/", false, "", "", ""},
	{"http://a\u200db/", false, "", "", ""},
	{"http://\u05d01.com/", true, "http", "xn--1-zhc.com", "/"},
}

var special = map[string]bool{"ftp": true, "file": true, "http": true, "https": true, "ws": true, "wss": true}

func TestAddressesAsJavaScriptReadsThem(t *testing.T) {
	for _, c := range webURLCases {
		want := ""
		switch {
		case !c.ok:
			want = "is not a valid URL"
		case c.scheme != "http" && c.scheme != "https":
			want = "must start with http:// or https://"
		case c.host == "":
			want = "has no host"
		}
		if got, _ := CheckURL(c.input); got != want {
			t.Errorf("CheckURL(%q) = %q, want %q", c.input, got, want)
		}
		u, ok := parseURL(c.input)
		if ok != c.ok || !ok {
			continue
		}
		if u.scheme != c.scheme || u.host != c.host {
			t.Errorf("parseURL(%q) = %q %q, want %q %q", c.input, u.scheme, u.host, c.scheme, c.host)
		}
		if special[c.scheme] && u.path != c.path {
			t.Errorf("parseURL(%q) path = %q, want %q", c.input, u.path, c.path)
		}
	}
}
