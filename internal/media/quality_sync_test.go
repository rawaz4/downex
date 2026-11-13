package media

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The extension and the desktop each repeat the naming rule in one helper
// (lib/quality.ts), tested with this package's tables (docs/SITE-MEDIA-SPEC.md,
// amendment 2026-10-23). Nothing is imported across the packages, so this test holds
// them together: the two copies of the helper and of its test are the same file, and
// the test's tables are qualityNameCases and trackNameCases, case for case.
func TestQualityHelperCopies(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(rel string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) {
			t.Skipf("%s is not in this checkout", rel)
		}
		if err != nil {
			t.Fatal(err)
		}
		return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	}
	for _, name := range []string{"quality.ts", "quality.test.ts"} {
		ext, ui := read("ext/src/lib/"+name), read("ui/frontend/src/lib/"+name)
		if !bytes.Equal(ext, ui) {
			t.Errorf("ext/src/lib/%s and ui/frontend/src/lib/%s differ", name, name)
		}
	}
	tests := string(read("ext/src/lib/quality.test.ts"))

	var want []string
	for _, c := range qualityNameCases {
		want = append(want, fmt.Sprintf("%d %d %s %v %q", c.width, c.height, number(c.fps), c.hdr, c.name))
	}
	got := tableRows(t, tests, "NAMES", `^\s*\[(-?\d+), (-?\d+), (-?[\d.]+), (true|false), '([^']*)'\],`,
		func(m []string) string { return fmt.Sprintf("%s %s %s %s %q", m[1], m[2], m[3], m[4], m[5]) })
	if !slices.Equal(got, want) {
		t.Errorf("quality.test.ts NAMES is not qualityNameCases:\n got %q\nwant %q", got, want)
	}

	want = want[:0]
	for _, c := range trackNameCases {
		want = append(want, fmt.Sprintf("%q %d %d %s %q", c.name, c.width, c.height, number(c.fps), c.want))
	}
	got = tableRows(t, tests, "TRACK_NAMES", `^\s*\['([^']*)', (-?\d+), (-?\d+), (-?[\d.]+), '([^']*)'\],`,
		func(m []string) string { return fmt.Sprintf("%q %s %s %s %q", m[1], m[2], m[3], m[4], m[5]) })
	if !slices.Equal(got, want) {
		t.Errorf("quality.test.ts TRACK_NAMES is not trackNameCases:\n got %q\nwant %q", got, want)
	}
}

// tableRows returns the rows of the TypeScript table `const <name>` (up to its "];"),
// one per line that row matches, as format makes them.
func tableRows(t *testing.T, src, name, row string, format func([]string) string) []string {
	t.Helper()
	_, body, ok := strings.Cut(src, "const "+name+":")
	if !ok {
		t.Fatalf("quality.test.ts has no table %s", name)
	}
	body, _, _ = strings.Cut(body, "\n];")
	re := regexp.MustCompile(row)
	var rows []string
	for line := range strings.SplitSeq(body, "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			rows = append(rows, format(m))
		}
	}
	return rows
}

// number formats a frame rate as TypeScript writes it: 24, 59.94.
func number(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
