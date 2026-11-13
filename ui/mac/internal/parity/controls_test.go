//go:build darwin && cgo

package parity

// [mac webkit] The native form controls app.css leaves to the browser (check boxes: the
// radios are visually hidden and the number fields are native NSViews), against WebKit's
// own: testdata/checks.html (16 and 14 px boxes, unchecked, checked, disabled both ways,
// focused both ways, app.css's accent and focus ring) snapped by testdata/wksnap.swift in a
// window that reports itself key, so that :focus-visible draws.

import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/kittest"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/mac"
	"downex/ui/views/viewstest"
)

const ctlW, ctlH = 260, 90

// renderChecks draws the page's boxes natively: focus is the box focused ("" none, else
// "f<row><col>" as the page names them).
func renderChecks(t *testing.T, theme, focus string, webkit bool) *image.RGBA {
	choice := &widgets.Choice{Choice: tokens.ChoiceLight}
	if theme == "dark" {
		choice = &widgets.Choice{Choice: tokens.ChoiceDark}
	}
	states := [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}, {false, false}, {true, false}}
	var focused *widgets.CheckBox
	root := widgets.NewScope(choice)
	root.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100), Padding: kit.Edges{Top: kit.Px(10), Start: kit.Px(10)}, RowGap: kit.Px(24)}
	root.Style.Background = kit.RGB(0xffffff) // the page's --surface
	if theme == "dark" {
		root.Style.Background = kit.RGB(0x171e1b)
	}
	for row, size := range []kit.DIP{16, 14} {
		line := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(40 - size), AlignItems: kit.AlignStart}}
		for col, st := range states {
			b := widgets.NewCheckBox(size, st[0], nil)
			b.Disabled = st[1]
			if focus == fmt.Sprintf("f%d%d", row, col) {
				focused = b
			}
			line.Add(b)
		}
		root.Add(line)
	}
	clock := kittest.NewClock()
	win := kit.NewWindow(root, kit.WindowOptions{Title: "checks", Palette: func(env kit.Environment) kit.Palette { return choice.Tokens(env).Palette() }, Now: clock.Now, Scheduler: clock})
	env := kit.Environment{Scale: kit.Scale{PixelsPerDIP: scale, Text: 1}, ReducedTransparency: true, WebKit: webkit}
	res, err := mac.NewResources()
	if err != nil {
		t.Skip(err)
	}
	h := hostcaptest.NewHost(kit.Size{W: ctlW, H: ctlH}, env, clock)
	loop := &kittest.Loop{Host: h.Host, Root: win, Canvas: &kittest.Canvas{}, Clock: clock}
	win.Attach(&realHost{Host: h, res: res})
	loop.Settle(60)
	if focused != nil {
		win.SetFocusVisible(true)
		win.Focus(focused)
		loop.Settle(60)
	}
	o, err := mac.NewOffscreen(res, ctlW*scale, ctlH*scale, scale, mac.OffscreenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	full := kit.RectAt(kit.Point{}, kit.Size{W: ctlW, H: ctlH})
	c, err := o.BeginFrame(full)
	if err != nil {
		t.Fatal(err)
	}
	win.Paint(c, full)
	if err := o.EndFrame(); err != nil {
		t.Fatal(err)
	}
	img, err := o.RGBA()
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// realHost is hostcaptest's host with the Mac's resources.
type realHost struct {
	*hostcaptest.Host
	res kit.Resources
}

func (h *realHost) Resources() kit.Resources { return h.res }

// TestParityCheckBoxes compares the check boxes with WebKit's, the Mac look (WebKit on)
// against the Chromium look the Windows window keeps (off), and holds the Mac look to under
// 1% of differing pixels in every state.
func TestParityCheckBoxes(t *testing.T) {
	s := newSetup(t)
	_, here, _, _ := runtime.Caller(0)
	dir := filepath.Dir(here)
	bin := filepath.Join(t.TempDir(), "wksnap")
	if b, err := exec.Command("swiftc", "-O", "-o", bin, filepath.Join(dir, "testdata", "wksnap.swift")).CombinedOutput(); err != nil {
		t.Skipf("swiftc: %v\n%s", err, b)
	}
	var rs []report
	for _, theme := range s.themes {
		for _, focus := range []string{"", "f04", "f05", "f14", "f15"} {
			name := "checks"
			js := ""
			if focus != "" {
				name += "-" + focus
				js = fmt.Sprintf("document.getElementById('%s').focus(); 1", focus)
			}
			refPath := filepath.Join(s.out, "ref", name+"-"+theme+".png")
			_ = os.MkdirAll(filepath.Dir(refPath), 0o755)
			url := "file://" + filepath.Join(dir, "testdata", "checks.html") + "#" + theme
			if b, err := exec.Command(bin, url, refPath, fmt.Sprint(ctlW), fmt.Sprint(ctlH), theme, js).CombinedOutput(); err != nil {
				t.Fatalf("wksnap: %v\n%s", err, b)
			}
			ref, err := readPNG(refPath)
			if err != nil {
				t.Fatal(err)
			}
			area := kit.R(0, 0, ctlW, ctlH)
			for _, webkit := range []bool{false, true} {
				look := "-chromium"
				if webkit {
					look = "-webkit"
				}
				r := s.sheet(t, name+look, theme, ref, renderChecks(t, theme, focus, webkit), area, kit.Point{})
				rs = append(rs, r)
				if webkit && r.Differing > 0.01 {
					t.Errorf("%s %s: %.2f%% of the pixels differ from WebKit's", name, theme, 100*r.Differing)
				}
			}
		}
	}
	writeReports(t, s, "checks.json", rs)
	_ = viewstest.WritePNG
}
