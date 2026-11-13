package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func hexSum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestSumFor(t *testing.T) {
	a, b := hexSum("a"), hexSum("b")
	list := a + "  yt-dlp.exe\n" + b + " *yt-dlp_linux\r\n" + "garbage line\n\n" + hexSum("c") + "  yt-dlp.exe.zip\n"
	if got, err := sumFor([]byte(list), "yt-dlp.exe"); err != nil || hex.EncodeToString(got[:]) != a {
		t.Errorf("yt-dlp.exe: %x, %v", got, err)
	}
	if got, err := sumFor([]byte(list), "yt-dlp_linux"); err != nil || hex.EncodeToString(got[:]) != b {
		t.Errorf("binary-mode line: %x, %v", got, err)
	}
	for name, l := range map[string]string{
		"missing":       list,
		"duplicate":     a + "  yt-dlp_macos\n" + b + "  yt-dlp_macos\n",
		"short hash":    a[:63] + "  yt-dlp_macos\n",
		"one space":     a + " yt-dlp_macos\n",
		"uppercase pad": strings.ToUpper(a) + "x  yt-dlp_macos\n",
		"empty":         "",
	} {
		if _, err := sumFor([]byte(l), "yt-dlp_macos"); !errors.Is(err, ErrChecksum) {
			t.Errorf("%s: %v, want ErrChecksum", name, err)
		}
	}
}

func TestParseSHA256File(t *testing.T) {
	want := "7e1ca9c8034b1b351cdc28b2d8f662557b63a65ab34a4748cd57d45ccdd8b11b"
	for _, name := range []string{"deno-x86_64-unknown-linux-gnu.zip", "deno-x86_64-pc-windows-msvc.zip"} {
		data, err := os.ReadFile(filepath.Join("testdata", name+".sha256sum"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := parseSHA256File(data, name)
		if err != nil || hex.EncodeToString(got[:]) != want {
			t.Errorf("%s: %x, %v", name, got, err)
		}
		// PowerShell 5 redirection writes UTF-16LE with a byte order mark.
		u := utf16.Encode([]rune(string(data)))
		le := []byte{0xff, 0xfe}
		for _, c := range u {
			le = append(le, byte(c), byte(c>>8))
		}
		if got, err := parseSHA256File(le, name); err != nil || hex.EncodeToString(got[:]) != want {
			t.Errorf("%s as UTF-16LE: %x, %v", name, got, err)
		}
	}
	// A single entry is taken whatever path it records; several are told apart by name.
	if got, err := parseSHA256File([]byte(hexSum("x")+"  target/release/deno\n"), "deno"); err != nil || hex.EncodeToString(got[:]) != hexSum("x") {
		t.Errorf("single entry: %x, %v", got, err)
	}
	multi := hexSum("x") + "  dir/deno\n" + hexSum("y") + "  dir/deno.exe\n"
	if got, err := parseSHA256File([]byte(multi), "deno.exe"); err != nil || hex.EncodeToString(got[:]) != hexSum("y") {
		t.Errorf("by name: %x, %v", got, err)
	}
	ps2 := "Algorithm : SHA256\r\nHash : " + strings.ToUpper(hexSum("p")) + "\r\nPath : C:\\x\\a.zip\r\n\r\nAlgorithm : SHA256\r\nHash : " + strings.ToUpper(hexSum("q")) + "\r\nPath : C:\\x\\b.zip\r\n"
	if got, err := parseSHA256File([]byte(ps2), "b.zip"); err != nil || hex.EncodeToString(got[:]) != hexSum("q") {
		t.Errorf("two PowerShell records: %x, %v", got, err)
	}
	for name, data := range map[string][]byte{
		"empty":          nil,
		"md5":            []byte("Algorithm : MD5\r\nHash : " + strings.Repeat("A", 32) + "\r\n"),
		"short hash":     []byte("Hash : " + strings.ToUpper(hexSum("p"))[:60] + "\r\n"),
		"not hex":        []byte("Hash : " + strings.Repeat("Z", 64) + "\r\n"),
		"ambiguous":      []byte(multi + hexSum("z") + "  other/deno.exe\n"),
		"no match":       []byte(multi),
		"odd UTF-16":     {0xff, 0xfe, 'H'},
		"NUL bytes":      []byte("Hash\x00 : x"),
		"just text":      []byte("hello\nworld\n"),
		"name, no hash":  []byte("Path : deno.exe\r\n"),
		"long hash line": []byte("Hash : " + strings.Repeat("a", 66) + "\r\n"),
	} {
		want := "deno.exe"
		if name == "no match" {
			want = "deno.zip"
		}
		if _, err := parseSHA256File(data, want); !errors.Is(err, ErrChecksum) {
			t.Errorf("%s: %v, want ErrChecksum", name, err)
		}
	}
}

func FuzzSumFor(f *testing.F) {
	f.Add([]byte(hexSum("a")+"  yt-dlp.exe\n"), "yt-dlp.exe")
	f.Add([]byte(hexSum("a")+" *yt-dlp.exe\r\n\n"), "yt-dlp.exe")
	f.Fuzz(func(t *testing.T, data []byte, name string) {
		if _, err := sumFor(data, name); err != nil && !errors.Is(err, ErrChecksum) {
			t.Fatalf("error not wrapping ErrChecksum: %v", err)
		}
	})
}

func FuzzParseSHA256File(f *testing.F) {
	for _, n := range []string{"deno-x86_64-unknown-linux-gnu.zip", "deno-x86_64-pc-windows-msvc.zip"} {
		if data, err := os.ReadFile(filepath.Join("testdata", n+".sha256sum")); err == nil {
			f.Add(data, n)
		}
	}
	f.Add([]byte{0xff, 0xfe, 'H', 0, ':', 0}, "deno.exe")
	f.Fuzz(func(t *testing.T, data []byte, name string) {
		if _, err := parseSHA256File(data, name); err != nil && !errors.Is(err, ErrChecksum) {
			t.Fatalf("error not wrapping ErrChecksum: %v", err)
		}
	})
}
