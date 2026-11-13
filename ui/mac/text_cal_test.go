//go:build darwin && cgo

package mac

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"downex/ui/kit"
)

// TestWebKitCalibration compares the engine with measurements taken in an offscreen
// WKWebView ($DOWNEX_WK_CAL: a folder with cases.json and the WebKit results wk.json, made
// by a throwaway program outside the repository). It only logs the table; the gates are
// TestWebKitWidths and the line box tests.
func TestWebKitCalibration(t *testing.T) {
	dir := os.Getenv("DOWNEX_WK_CAL")
	if dir == "" {
		t.Skip("DOWNEX_WK_CAL not set")
	}
	type tcase struct {
		ID     string  `json:"id"`
		Text   string  `json:"text"`
		Fam    string  `json:"fam"`
		Size   float32 `json:"size"`
		Weight float32 `json:"weight"`
		LH     float32 `json:"lh"`
		LS     float32 `json:"lsem"`
		Italic bool    `json:"italic"`
		Tnum   bool    `json:"tnum"`
		Width  float32 `json:"width"`
		OW     string  `json:"ow"`
		Offs   []int   `json:"offsets"`
	}
	type result struct {
		ID         string    `json:"id"`
		Width      float64   `json:"width"`
		Height     float64   `json:"height"`
		Baseline   float64   `json:"baseline"`
		LineStarts []int     `json:"lineStarts"`
		Offs       []float64 `json:"offs"`
	}
	var cases []tcase
	var res []result
	read := func(name string, v any) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	read("cases.json", &cases)
	read("wk.json", &res)
	byID := map[string]result{}
	for _, r := range res {
		byID[r.ID] = r
	}
	e := newEngine(t)
	var worst float64
	for _, c := range cases {
		r := byID[c.ID]
		st := kit.TextStyle{Family: c.Fam, Size: c.Size, Weight: c.Weight, LineHeight: c.LH, LetterSpacing: c.LS, Italic: c.Italic, Tabular: c.Tnum}
		o := kit.TextOptions{}
		if c.Width > 0 {
			o.MaxWidth, o.Wrap = c.Width, kit.TextWrapWords
			if c.OW == "anywhere" {
				o.Wrap = kit.TextWrapAnywhere
			}
		}
		l := lay(t, e, c.Text, st, o)
		m := l.Metrics()
		line := ""
		if c.Width > 0 {
			var starts []int
			for _, ln := range l.lines {
				starts = append(starts, ln.start)
			}
			line = fmt.Sprintf(" lines %v WebKit %v", starts, r.LineStarts)
			if fmt.Sprint(starts) != fmt.Sprint(r.LineStarts) {
				line += " DIFFERENT"
			}
		} else {
			worst = max(worst, math.Abs(float64(m.Width)-r.Width))
		}
		t.Logf("%-12s w %9.4f wk %9.4f (%+.4f) h %5.1f wk %5.1f base %5.1f wk %5.1f%s", c.ID, m.Width, r.Width, float64(m.Width)-r.Width, m.Height, r.Height, m.Baseline, r.Baseline, line)
		for i, off := range c.Offs {
			x, _ := l.Caret(len(string([]rune(c.Text)[:off])))
			if math.Abs(float64(x.X)-r.Offs[i]) > 0.01 {
				t.Logf("   offset %d: %.4f wk %.4f", off, x.X, r.Offs[i])
			}
		}
	}
	t.Logf("worst one-line width difference %.4f px", worst)
}
