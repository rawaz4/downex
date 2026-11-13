package text_test

import (
	"testing"

	"downex/ui/core/text"
)

// The tables of ui/frontend/src/lib/text.test.ts (its cases on categories.ts and display.ts
// are in those packages' tests).

const rlo = "\u202E"

func TestEscapeBidiMakesEveryControlVisible(t *testing.T) {
	if got := text.EscapeBidi("invoice" + rlo + "fdp.exe"); got != "invoice⟨U+202E⟩fdp.exe" {
		t.Errorf("EscapeBidi = %q", got)
	}
	all := "\u202A\u202B\u202C\u202D\u202E\u2066\u2067\u2068\u2069\u200E\u200F\u061C"
	want := "⟨U+202A⟩⟨U+202B⟩⟨U+202C⟩⟨U+202D⟩⟨U+202E⟩⟨U+2066⟩⟨U+2067⟩⟨U+2068⟩⟨U+2069⟩⟨U+200E⟩⟨U+200F⟩⟨U+061C⟩"
	if got := text.EscapeBidi(all); got != want {
		t.Errorf("EscapeBidi(all) = %q, want %q", got, want)
	}
	if !text.HasBidiControls(all) {
		t.Error("HasBidiControls(all) = false")
	}
	for _, s := range []string{"plain name.pdf", "تقرير.pdf"} {
		if got := text.EscapeBidi(s); got != s {
			t.Errorf("EscapeBidi(%q) = %q", s, got)
		}
	}
}

func TestIsolate(t *testing.T) {
	if got := text.Isolate("(final) report.pdf"); got != "\u2068(final) report.pdf\u2069" {
		t.Errorf("Isolate = %q", got)
	}
	// an embedded PDI cannot close the isolate early
	if got := text.Isolate("a\u2069b"); got != "\u2068a⟨U+2069⟩b\u2069" {
		t.Errorf("Isolate = %q", got)
	}
}

func TestSplitExtension(t *testing.T) {
	for _, c := range [][3]string{
		{"وزە بۆ کردنی کارێک ؟.mp4", "وزە بۆ کردنی کارێک ؟", ".mp4"},
		{"Win.10.Aio.19045.7807.Multi.iso", "Win.10.Aio.19045.7807.Multi", ".iso"},
		{"archive.tar.gz", "archive.tar", ".gz"},
	} {
		if stem, ext := text.SplitExtension(c[0]); stem != c[1] || ext != c[2] {
			t.Errorf("SplitExtension(%q) = %q %q, want %q %q", c[0], stem, ext, c[1], c[2])
		}
	}
}

func TestSplitExtensionKeepsOtherNamesWhole(t *testing.T) {
	for _, name := range []string{"README", ".gitignore", "notes.", "ڕاپۆرت.پدف", "file.name with space"} {
		if stem, ext := text.SplitExtension(name); stem != name || ext != "" {
			t.Errorf("SplitExtension(%q) = %q %q", name, stem, ext)
		}
	}
}

func TestSplitExtensionNeverLosesCharacters(t *testing.T) {
	for _, name := range []string{"a.b", "invoice⟨U+202E⟩fdp.exe", "2015_12_17 لە سوید.mp4"} {
		if stem, ext := text.SplitExtension(name); stem+ext != name {
			t.Errorf("SplitExtension(%q) = %q %q", name, stem, ext)
		}
	}
}

// ---- the JavaScript rules the ports lean on (not in text.test.ts) ----

func TestTrimJS(t *testing.T) {
	for in, want := range map[string]string{
		" a ": "a", "\t\n\v\f\r a": "a", "\u00a0\ufeffa\u3000\u2028": "a", "\u200ba": "\u200ba", "": "",
	} {
		if got := text.TrimJS(in); got != want {
			t.Errorf("TrimJS(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeURIComponent(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"my%20notes.pdf", "my notes.pdf", true},
		{"invoice%E2%80%AEfdp.exe", "invoice" + rlo + "fdp.exe", true},
		{"%2F%3F%23", "/?#", true},
		{"100%.zip", "", false},
		{"%zz", "", false},
		{"%E2%80", "", false},    // a character cut short
		{"%C0%AF", "", false},    // overlong
		{"%ED%A0%80", "", false}, // a surrogate
		{"%EF%BF%BD", "\ufffd", true},
		{"plain", "plain", true},
	} {
		got, ok := text.DecodeURIComponent(c.in)
		if ok != c.ok || ok && got != c.want {
			t.Errorf("DecodeURIComponent(%q) = %q %v, want %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestLowerJS(t *testing.T) {
	for in, want := range map[string]string{"MP4": "mp4", "\u0130": "i\u0307", "ΟΔΟ\u03a3": "οδος", "\u03a3Α": "σα", "AppImage": "appimage"} {
		if got := text.LowerJS(in); got != want {
			t.Errorf("LowerJS(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseURL(t *testing.T) {
	for _, c := range []struct {
		in                       string
		host, hostname, pathname string
		ok                       bool
	}{
		{"https://cdn.example.net:8443/a", "cdn.example.net:8443", "cdn.example.net", "/a", true},
		{"https://cdn.example.com/files/my%20notes.pdf", "cdn.example.com", "cdn.example.com", "/files/my%20notes.pdf", true},
		{"http://user:pw@host:8080/x", "host:8080", "host", "/x", true},
		{"https://[::1]:8443/", "[::1]:8443", "[::1]", "/", true},
		{"https://bücher.example/", "xn--bcher-kva.example", "xn--bcher-kva.example", "/", true},
		{"HTTPS://WWW.Example.COM:443/x?y#z", "www.example.com", "www.example.com", "/x", true},
		{"http://0x7f.1/", "127.0.0.1", "127.0.0.1", "/", true},
		{"https://a.example/a/./b/../c", "a.example", "a.example", "/a/c", true},
		{`https:\\a.example\b\c`, "a.example", "a.example", "/b/c", true},
		{"https://a.example/my file.pdf", "a.example", "a.example", "/my%20file.pdf", true},
		{"file:///C:/x", "", "", "/C:/x", true},
		{"not a url", "", "", "", false},
		{"", "", "", "", false},
		{"https://", "", "", "", false},
		{"https://a b/", "", "", "", false},
		{"https://a.example:99999/", "", "", "", false},
		{"https://[::1/", "", "", "", false},
	} {
		u, err := text.ParseURL(c.in)
		if (err == nil) != c.ok {
			t.Errorf("ParseURL(%q) error %v, want ok %v", c.in, err, c.ok)
			continue
		}
		if !c.ok {
			continue
		}
		if u.Host() != c.host || u.Hostname() != c.hostname || u.Pathname() != c.pathname {
			t.Errorf("ParseURL(%q) = host %q hostname %q path %q, want %q %q %q", c.in, u.Host(), u.Hostname(), u.Pathname(), c.host, c.hostname, c.pathname)
		}
	}
}
