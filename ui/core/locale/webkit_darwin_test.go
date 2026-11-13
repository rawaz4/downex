//go:build darwin && cgo

package locale

import (
	"encoding/json"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// webkitIntl is what Intl gave in a WKWebView on a Mac (testdata/webkit-intl-macos.json:
// testdata/webkit-intl.js run by testdata/webkit-intl.swift, whose first lines say how): the
// formats of format.ts's six styles, its numbers and media.ts's language names, for many
// locales and zones. DOWNEX_WEBKIT_INTL names another such file (more locales and zones).
type webkitIntl struct {
	MacOS string `json:"macos"`
	Meta  struct {
		Language string `json:"language"`
	} `json:"meta"`
	Instants []float64 `json:"instants"`
	Dates    []struct {
		Tag, Zone, Style string
		Error            string    `json:"error"`
		Outs             []*string `json:"outs"`
		Years            []*string `json:"years"`
	} `json:"dates"`
	Numbers []struct {
		Tag    string   `json:"tag"`
		Digits int      `json:"digits"`
		Error  string   `json:"error"`
		Ins    []string `json:"ins"`
		Outs   []string `json:"outs"`
	} `json:"numbers"`
	Names []struct {
		Tag string          `json:"tag"`
		Out json.RawMessage `json:"out"`
	} `json:"names"`
}

func loadWebKitIntl(t *testing.T) *webkitIntl {
	t.Helper()
	path := os.Getenv("DOWNEX_WEBKIT_INTL")
	if path == "" {
		path = "testdata/webkit-intl-macos.json"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var w webkitIntl
	if err := json.Unmarshal(data, &w); err != nil {
		t.Fatal(err)
	}
	if w.MacOS != "" {
		if v, err := syscall.Sysctl("kern.osproductversion"); err == nil && major(v) != major(w.MacOS) {
			t.Skipf("the WebKit outputs are from macOS %s, this is macOS %s (another ICU)", w.MacOS, v)
		}
	}
	return &w
}

func major(v string) string { m, _, _ := strings.Cut(v, "."); return m }

var styleByName = map[string]Style{"time": Time, "clock": Clock, "monthDay": MonthDay, "date": Date, "ymd": YMD, "full": Full}

// TestMatchesWebKit compares the darwin backend with WebKit's Intl on the same Mac, case for
// case: every date style in every locale and zone, the years of formatToParts, numbers with 0
// to 3 and 20 fraction digits, and English language names.
func TestMatchesWebKit(t *testing.T) {
	w := loadWebKitIntl(t)
	user := UserLocale()
	failures := 0
	shown := map[string]int{}
	fail := func(format string, args ...any) {
		failures++
		// at most 3 differences of a kind (the first two arguments: a tag and a zone or call)
		key := ""
		if len(args) >= 2 {
			key = strings.TrimSpace(strings.SplitN(strings.TrimSpace(format), " ", 3)[0]) + "|" + toString(args[0]) + "|" + toString(args[1])
		}
		if shown[key]++; shown[key] <= 3 && failures <= 400 {
			t.Errorf(format, args...)
		}
	}
	compared := 0
	for _, d := range w.Dates {
		if (d.Tag == "xx-YY" || d.Tag == "und") && w.Meta.Language != user {
			continue // formatted in the user's language: only when the WebKit run's was this user's
		}
		zone := d.Zone
		f, err := New(d.Tag, zone)
		if d.Error != "" {
			if err == nil {
				fail("New(%q, %q) accepted, WebKit threw %s", d.Tag, zone, d.Error)
			}
			continue
		}
		if err != nil {
			fail("New(%q, %q): %v", d.Tag, zone, err)
			continue
		}
		style, ok := styleByName[d.Style]
		if !ok {
			t.Fatalf("style %q", d.Style)
		}
		for i, ms := range w.Instants {
			want := ""
			if d.Outs[i] != nil {
				want = *d.Outs[i]
			}
			compared++
			if got := f.Date(ms, style); got != want {
				fail("%s %q %s(%v) = %q, WebKit %q", d.Tag, zone, d.Style, ms, got, want)
			}
			if d.Years != nil {
				want = ""
				if d.Years[i] != nil {
					want = *d.Years[i]
				}
				if got := f.Year(ms); got != want {
					fail("%s %q Year(%v) = %q, WebKit %q", d.Tag, zone, ms, got, want)
				}
			}
		}
	}
	for _, n := range w.Numbers {
		f, err := New(n.Tag, "UTC")
		if n.Error != "" {
			continue
		}
		if err != nil {
			fail("New(%q): %v", n.Tag, err)
			continue
		}
		for i, in := range n.Ins {
			v := parseJSNumber(t, in)
			compared++
			if got := f.Number(v, n.Digits); got != n.Outs[i] {
				fail("%s Number(%s, %d) = %q, WebKit %q", n.Tag, in, n.Digits, got, n.Outs[i])
			}
		}
	}
	for _, c := range w.Names {
		var want string
		throws := json.Unmarshal(c.Out, &want) != nil
		got, ok := LanguageName(c.Tag)
		compared++
		switch {
		case throws && ok:
			fail("LanguageName(%q) = %q, WebKit throws", c.Tag, got)
		case !throws && !ok && want != c.Tag:
			fail("LanguageName(%q) has none, WebKit %q", c.Tag, want)
		case !throws && ok && got != want:
			fail("LanguageName(%q) = %q, WebKit %q", c.Tag, got, want)
		}
	}
	t.Logf("%d cases compared on %s, %d differ", compared, runtime.GOARCH, failures)
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func parseJSNumber(t *testing.T, s string) float64 {
	switch s {
	case "NaN":
		return math.NaN()
	case "Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	case "-0":
		return math.Copysign(0, -1)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
