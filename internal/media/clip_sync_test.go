package media

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The extension repeats ParseClipTime in ext/src/lib/clip.ts (the in-page panel's Part row,
// docs/MEDIA-SPEC.md §8). This test holds its table CLIP_TIMES (clip.test.ts, which the
// extension's tests run against its parser) to this package's parser, row for row.
func TestClipTimesMatchExtension(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "ext", "src", "lib", "clip.test.ts"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("ext/src/lib/clip.test.ts is not in this checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	rows := tableRows(t, string(b), "CLIP_TIMES", `^\s*\['([^']*)', (-?\d+)\],`, func(m []string) string { return m[1] + "\x00" + m[2] })
	if len(rows) < 20 {
		t.Fatalf("CLIP_TIMES has %d rows", len(rows))
	}
	for _, r := range rows {
		in, want, _ := bytes.Cut([]byte(r), []byte{0})
		ms, _ := strconv.ParseInt(string(want), 10, 64)
		d, err := ParseClipTime(string(in))
		got := d.Milliseconds()
		if err != nil {
			got = -1
		}
		if got != ms {
			t.Errorf("ParseClipTime(%q) = %d ms, CLIP_TIMES says %d", in, got, ms)
		}
	}
}
