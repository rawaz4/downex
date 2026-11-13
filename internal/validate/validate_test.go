package validate

import (
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

func TestURLMethodBody(t *testing.T) {
	for _, u := range []string{"http://example.test/f", "https://example.test:8443/a?b=c#d"} {
		if err := URL(u); err != nil {
			t.Errorf("URL(%q) = %v", u, err)
		}
	}
	for _, u := range []string{"", "ftp://example.test/f", "http:///f", "example.test/f", "http://a\nb/",
		"http://example.test/" + strings.Repeat("a", MaxURLBytes)} {
		if err := URL(u); err == nil {
			t.Errorf("URL(%q) accepted", u)
		}
	}
	for in, want := range map[string]string{"": "GET", "get": "GET", "Post": "POST"} {
		if got, err := Method(in); err != nil || got != want {
			t.Errorf("Method(%q) = %q %v", in, got, err)
		}
	}
	if _, err := Method("PUT"); err == nil {
		t.Error("PUT accepted")
	}
	if Body(MaxBodyBytes, "POST") != nil || Body(0, "GET") != nil || Body(1, "GET") == nil || Body(MaxBodyBytes+1, "POST") == nil {
		t.Error("Body limits")
	}
}

func TestHeadersAndFilename(t *testing.T) {
	h, err := Headers([]*downexv1.Header{{Name: "cookie", Value: "a=b"}, {Name: "User-Agent", Value: "UA\tx"}})
	if err != nil || h.Get("Cookie") != "a=b" || h.Get("User-Agent") != "UA\tx" {
		t.Fatalf("Headers = %v %v", h, err)
	}
	if h, err := Headers(nil); h != nil || err != nil {
		t.Fatalf("no headers: %v %v", h, err)
	}
	for _, bad := range [][2]string{{"Host", "SECRET1"}, {"range", "bytes=0-SECRET2"}, {"X", "SECRET3\r\nb"}, {"Bad Name", "SECRET4"}, {"", "SECRET5"},
		{"Cookie", strings.Repeat("c", MaxHeaderBytes)}} {
		_, err := Headers([]*downexv1.Header{{Name: bad[0], Value: bad[1]}})
		if err == nil {
			t.Errorf("header %q accepted", bad[0])
		} else if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "ccc") {
			t.Errorf("message repeats the value: %v", err)
		}
	}
	if name, err := Filename("a:b?<c>.txt"); err != nil || name != "a_b__c_.txt" {
		t.Errorf("Filename = %q %v", name, err)
	}
	for _, bad := range []string{"../x", `a\b`, "..", "."} {
		if _, err := Filename(bad); err == nil {
			t.Errorf("Filename(%q) accepted", bad)
		}
	}
}

func TestMIME(t *testing.T) {
	for in, want := range map[string]string{
		"":                                "",
		"video/mp4":                       "video/mp4",
		"  Application/ZIP ; name=a.zip ": "application/zip",
		"application/vnd.ms-excel":        "application/vnd.ms-excel",
		"application/epub+zip":            "application/epub+zip",
		"text":                            "", // not type/subtype: dropped
		"video/":                          "",
		"/mp4":                            "",
		"a/b/c":                           "",
		"vid eo/mp4":                      "",
		"vidéo/mp4":                       "",
		"-video/mp4":                      "",
		"application/x-msdownload;q=0.9":  "application/x-msdownload",
		strings.Repeat("a", 127) + "/b":   strings.Repeat("a", 127) + "/b",
		strings.Repeat("a", 128) + "/b":   "",
	} {
		if got, err := MIME(in); err != nil || got != want {
			t.Errorf("MIME(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"video/mp4\r\nX: y", "video/mp4\x00", "video/mp4\t", "a/" + strings.Repeat("b", MaxMIMEBytes)} {
		if got, err := MIME(bad); err == nil {
			t.Errorf("MIME(%q) = %q, want an error", bad, got)
		}
	}
}

func TestExpectedSize(t *testing.T) {
	for in, want := range map[int64]int64{-1: 0, 0: 0, 1: 1, 1 << 50: 1 << 50} {
		if got, err := ExpectedSize(in); err != nil || got != want {
			t.Errorf("ExpectedSize(%d) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ExpectedSize(-2); err == nil {
		t.Error("ExpectedSize(-2) accepted")
	}
}
