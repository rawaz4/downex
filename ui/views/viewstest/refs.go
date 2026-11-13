package viewstest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"downex/ui/internal/research"
	"downex/ui/kit"
)

// The 0.9.2 references (ref-0.9.2: today's window rendered by measure-dialogs.mjs's demo in
// headless Edge, every fixture state and the main window at 1200×760, 1190×752, 960×608,
// 900×600 and 1920×1080, at scale 1 and 1.25, 16 and 20 px text, light and dark) and the
// comparisons with native renders.

// The references and the comparisons live in the research folder (ui/internal/research:
// $DOWNEX_RESEARCH, else the one beside the repository), never in a path compiled in:
// $DOWNEX_REF_092 and $DOWNEX_VIEWS_SHOTS name other folders. Where there is no such
// folder, LoadRefs answers ErrNoRefs and the comparisons skip.
var (
	refsRel  = filepath.Join(research.NativeUI, "ref-0.9.2")
	shotsRel = research.NativeUI
)

// ErrNoRefs is LoadRefs' answer when the references are not on this machine (tests skip).
var ErrNoRefs = errors.New("viewstest: the 0.9.2 references are not here")

// RefDir is the references' folder: $DOWNEX_REF_092, else ref-0.9.2 in the research folder
// ("" when it is not known).
func RefDir() string {
	if d := os.Getenv("DOWNEX_REF_092"); d != "" {
		return d
	}
	return besideRepo(refsRel)
}

// ShotDir is where the comparisons are written: $DOWNEX_VIEWS_SHOTS, else the research
// folder's native-ui ("" when it is not known: the current folder).
func ShotDir() string {
	if d := os.Getenv("DOWNEX_VIEWS_SHOTS"); d != "" {
		return d
	}
	return besideRepo(shotsRel)
}

// besideRepo is rel in the research folder.
func besideRepo(rel string) string { return research.Beside(rel) }

// Refs is the references' index.
type Refs struct {
	Dir   string
	Shots []*Shot `json:"shots"`
}

// Shot is one reference screenshot as the index has it.
type Shot struct {
	// File is the PNG's path in the folder:
	// screens/<kind>/<state>/<state>__<w>x<h>__<scale>x__<font>px__<theme>.png.
	File string `json:"file"`
	// Kind is "main", "state" (a dialog state) or "progress".
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	State string `json:"state"`
	// Dialog is the dialog's name ("Remove", "Main window").
	Dialog string `json:"dialog"`
	CSS    struct {
		W int `json:"w"`
		H int `json:"h"`
	} `json:"css"`
	Scale float64 `json:"scale"`
	Font  int     `json:"font"`
	Theme string  `json:"theme"`
	// Measure is a dialog state's dialog box (CSS px).
	Measure *Measure `json:"measure"`
	// Layout is the main window's element boxes.
	Layout *Layout `json:"layout"`
}

// Measure is the dialog's box in a dialog state (measure-dialogs.mjs's check 7).
type Measure struct {
	W      float64 `json:"w"`
	H      float64 `json:"h"`
	Top    float64 `json:"top"`
	Bottom float64 `json:"bottom"`
	MaxH   float64 `json:"maxH"`
	Fits   bool    `json:"fits"`
	Title  string  `json:"title"`
}

// Layout is the main window's element boxes.
type Layout struct {
	Rows    int      `json:"rows"`
	Columns []string `json:"columns"`
	Nodes   []Node   `json:"nodes"`
}

// Node is an element's box in CSS px: El is "tag.class.class", D its depth.
type Node struct {
	El   string  `json:"el"`
	D    int     `json:"d"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
	Text string  `json:"text"`
	Role string  `json:"role"`
}

// Rect is the node's box in DIPs.
func (n Node) Rect() kit.Rect {
	return kit.R(kit.DIP(n.X), kit.DIP(n.Y), kit.DIP(n.X+n.W), kit.DIP(n.Y+n.H))
}

var (
	refsOnce sync.Once
	refs     *Refs
	refsErr  error
)

// LoadRefs reads the index once (it is 4 MB); ErrNoRefs when the folder is missing.
func LoadRefs() (*Refs, error) {
	refsOnce.Do(func() {
		dir := RefDir()
		b, err := os.ReadFile(filepath.Join(dir, "index.json"))
		if err != nil {
			refsErr = fmt.Errorf("%w: %v", ErrNoRefs, err)
			return
		}
		b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
		var r Refs
		if err := json.Unmarshal(b, &r); err != nil {
			refsErr = fmt.Errorf("viewstest: the references' index: %w", err)
			return
		}
		r.Dir = dir
		refs = &r
	})
	return refs, refsErr
}

// ScaleText is a scale as the names write it ("1x", "1.25x").
func ScaleText(s float64) string { return strconv.FormatFloat(s, 'f', -1, 64) + "x" }

// RefName is a reference's file name: <state>__<w>x<h>__<scale>x__<font>px__<theme>.png.
func RefName(state string, w, h int, scale float64, font int, theme string) string {
	return fmt.Sprintf("%s__%dx%d__%s__%dpx__%s.png", state, w, h, ScaleText(scale), font, theme)
}

// NativeName is a comparison's file name: native-s2-<lane>-<state>-<theme>-<width>-<scale>.png
// (the 20 px text runs add "-20px" to the state).
func NativeName(lane, state, theme string, width int, scale float64) string {
	return fmt.Sprintf("native-s2-%s-%s-%s-%d-%s.png", lane, state, theme, width, ScaleText(scale))
}

// Find is the reference of a state (kind "main", "state" or "progress"; name "main", a
// fixture's name or a progress view's) at a size, scale, text size and theme.
func (r *Refs) Find(kind, name string, w, h int, scale float64, font int, theme string) (*Shot, bool) {
	for _, s := range r.Shots {
		if s.Kind == kind && s.Name == name && s.CSS.W == w && s.CSS.H == h && s.Scale == scale && s.Font == font && s.Theme == theme {
			return s, true
		}
	}
	return nil, false
}

// For is the reference matching a harness's options (Fixture's state, else the main window).
func (r *Refs) For(o Options) (*Shot, bool) {
	kind, name := "main", "main"
	if o.Fixture != "" {
		kind, name = "state", o.Fixture
		if strings.HasPrefix(o.Fixture, "pw-") {
			kind = "progress"
		}
	}
	w, h, scale, font, theme := o.Width, o.Height, float64(o.Scale), o.Font, o.Theme
	if w == 0 {
		w, h = 1200, 760
	}
	if scale == 0 {
		scale = 1
	}
	if font == 0 {
		font = 16
	}
	if theme == "" {
		theme = "light"
	}
	return r.Find(kind, name, w, h, scale, font, theme)
}

// Image reads a reference's PNG.
func (r *Refs) Image(s *Shot) (image.Image, error) {
	f, err := os.Open(filepath.Join(r.Dir, filepath.FromSlash(s.File)))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// Nodes are the main window's elements matching sel: "tag.class" with every class named
// (a missing tag matches any), and text, when not "", a substring of the element's text.
func (s *Shot) Nodes(sel, text string) []Node {
	if s.Layout == nil {
		return nil
	}
	tag, classes := parseSel(sel)
	var out []Node
	for _, n := range s.Layout.Nodes {
		ntag, ncls := parseSel(n.El)
		if tag != "" && tag != ntag {
			continue
		}
		ok := true
		for _, c := range classes {
			if !contains(ncls, c) {
				ok = false
				break
			}
		}
		if ok && (text == "" || strings.Contains(n.Text, text)) {
			out = append(out, n)
		}
	}
	return out
}

func parseSel(s string) (tag string, classes []string) {
	parts := strings.Split(s, ".")
	for _, p := range parts[1:] {
		if p != "" {
			classes = append(classes, p)
		}
	}
	return parts[0], classes
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// DialogBox is a dialog state's dialog box in DIPs: its measured width, height and top,
// centred in the window as Dialog.svelte's backdrop centres it.
func (s *Shot) DialogBox() (kit.Rect, bool) {
	if s.Measure == nil || s.Measure.W == 0 {
		return kit.Rect{}, false
	}
	x := (float64(s.CSS.W) - s.Measure.W) / 2
	return kit.R(kit.DIP(x), kit.DIP(s.Measure.Top), kit.DIP(x+s.Measure.W), kit.DIP(s.Measure.Top+s.Measure.H)), true
}

// Probe names a native element's box to compare with a reference element: Selector and Text
// pick the reference node (Shot.Nodes, the Nth match, 0 the first), or Dialog takes the
// state's measured dialog box.
type Probe struct {
	Name     string
	Selector string
	Text     string
	Nth      int
	Dialog   bool
	Native   kit.Rect
}

// Delta is a probe's result: the boxes and their differences in DIPs (native − reference).
type Delta struct {
	Name           string
	Ref, Native    kit.Rect
	DX, DY, DW, DH kit.DIP
	Found          bool
}

// Max is the largest of the four differences' sizes.
func (d Delta) Max() kit.DIP {
	return kit.DIP(math.Max(math.Max(math.Abs(float64(d.DX)), math.Abs(float64(d.DY))), math.Max(math.Abs(float64(d.DW)), math.Abs(float64(d.DH)))))
}

func (d Delta) String() string {
	if !d.Found {
		return d.Name + ": no reference box"
	}
	return fmt.Sprintf("%s: dx %+.1f dy %+.1f dw %+.1f dh %+.1f DIP (ref %v, native %v)", d.Name, d.DX, d.DY, d.DW, d.DH, d.Ref, d.Native)
}

// BoxDeltas compares the probes' native boxes with the reference's.
func BoxDeltas(s *Shot, probes []Probe) []Delta {
	out := make([]Delta, len(probes))
	for i, p := range probes {
		d := Delta{Name: p.Name, Native: p.Native}
		var ref kit.Rect
		switch {
		case p.Dialog:
			ref, d.Found = s.DialogBox()
		default:
			if ns := s.Nodes(p.Selector, p.Text); p.Nth < len(ns) {
				ref, d.Found = ns[p.Nth].Rect(), true
			}
		}
		if d.Found {
			d.Ref = ref
			d.DX, d.DY = p.Native.Min.X-ref.Min.X, p.Native.Min.Y-ref.Min.Y
			d.DW, d.DH = p.Native.Width()-ref.Width(), p.Native.Height()-ref.Height()
		}
		out[i] = d
	}
	return out
}

// Comparison is a reference and a native render of the same area, side by side.
type Comparison struct {
	// Sheet is reference | native | difference.
	Sheet *image.RGBA
	// Mean is the mean absolute difference per channel (0–255) over the area; Differing the
	// share of pixels whose largest channel difference is above Threshold.
	Mean      float64
	Differing float64
	Threshold uint8
}

// Compare crops the reference to refArea and the native render to an area of the same size at
// nativeAt (DIPs, scaled by scale into device pixels; the same point when the layouts agree),
// and puts them side by side with their difference (amplified 4 times on black, so that small
// shifts show).
func Compare(ref, native image.Image, refArea kit.Rect, nativeAt kit.Point, scale float32) Comparison {
	px := func(v kit.DIP) int { return int(math.Round(float64(v * scale))) }
	r := image.Rect(px(refArea.Min.X), px(refArea.Min.Y), px(refArea.Max.X), px(refArea.Max.Y)).Intersect(ref.Bounds())
	off := image.Pt(px(nativeAt.X), px(nativeAt.Y)).Sub(image.Pt(px(refArea.Min.X), px(refArea.Min.Y)))
	n := r.Add(off).Intersect(native.Bounds())
	r = n.Sub(off)
	w, h := r.Dx(), r.Dy()
	const gap = 8
	sheet := image.NewRGBA(image.Rect(0, 0, max(3*w+2*gap, 1), max(h, 1)))
	draw.Draw(sheet, sheet.Bounds(), &image.Uniform{color.RGBA{128, 128, 128, 255}}, image.Point{}, draw.Src)
	draw.Draw(sheet, image.Rect(0, 0, w, h), ref, r.Min, draw.Src)
	draw.Draw(sheet, image.Rect(w+gap, 0, 2*w+gap, h), native, n.Min, draw.Src)
	c := Comparison{Sheet: sheet, Threshold: 24}
	a, b := rgbaOf(ref), rgbaOf(native)
	var sum float64
	var differing int
	for y := 0; y < h; y++ {
		ia := a.PixOffset(r.Min.X, r.Min.Y+y)
		ib := b.PixOffset(n.Min.X, n.Min.Y+y)
		is := sheet.PixOffset(2*w+2*gap, y)
		for x := 0; x < w; x, ia, ib, is = x+1, ia+4, ib+4, is+4 {
			dr, dg, db := absDiff(a.Pix[ia], b.Pix[ib]), absDiff(a.Pix[ia+1], b.Pix[ib+1]), absDiff(a.Pix[ia+2], b.Pix[ib+2])
			sum += float64(dr) + float64(dg) + float64(db)
			if max(dr, dg, db) > c.Threshold {
				differing++
			}
			sheet.Pix[is], sheet.Pix[is+1], sheet.Pix[is+2], sheet.Pix[is+3] = amp4(dr), amp4(dg), amp4(db), 255
		}
	}
	if n := w * h; n > 0 {
		c.Mean = sum / float64(3*n)
		c.Differing = float64(differing) / float64(n)
	}
	return c
}

// amp4 is a difference made 4 times brighter for the sheet's difference panel.
func amp4(v uint8) uint8 { return uint8(min(int(v)*4, 255)) }

// rgbaOf is img as an *image.RGBA (premultiplied, as color.RGBAModel converts each pixel),
// converted once so that the comparisons read bytes, not a colour per pixel.
func rgbaOf(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	b := img.Bounds()
	out := image.NewRGBA(b)
	draw.Draw(out, b, img, b.Min, draw.Src)
	return out
}

func absDiff(a, b uint8) uint8 {
	if a > b {
		return a - b
	}
	return b - a
}

// WritePNG writes an image.
func WritePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// BestShift is the shift of the native crop (device pixels, within ±maxPx) that best matches
// the reference crop, and the mean absolute difference (0–255, luminance) there and unshifted:
// a native element drawn one pixel low shows as dy = 1. The areas are as Compare's.
func BestShift(ref, native image.Image, refArea kit.Rect, nativeAt kit.Point, scale float32, maxPx int) (dx, dy int, best, unshifted float64) {
	px := func(v kit.DIP) int { return int(math.Round(float64(v * scale))) }
	r := image.Rect(px(refArea.Min.X), px(refArea.Min.Y), px(refArea.Max.X), px(refArea.Max.Y))
	at := image.Pt(px(nativeAt.X), px(nativeAt.Y))
	// The luminance (color.GrayModel's) of the reference crop and of the native crop grown by
	// maxPx, read once; -1 outside an image.
	lumOf := func(img image.Image, area image.Rectangle) []float64 {
		src := rgbaOf(img)
		out := make([]float64, area.Dx()*area.Dy())
		i := 0
		for y := area.Min.Y; y < area.Max.Y; y++ {
			for x := area.Min.X; x < area.Max.X; x, i = x+1, i+1 {
				if !(image.Point{x, y}.In(src.Bounds())) {
					out[i] = -1
					continue
				}
				o := src.PixOffset(x, y)
				r, g, b := uint32(src.Pix[o])*0x101, uint32(src.Pix[o+1])*0x101, uint32(src.Pix[o+2])*0x101
				out[i] = float64(uint8((19595*r + 38470*g + 7471*b + 1<<15) >> 24))
			}
		}
		return out
	}
	ra := lumOf(ref, r)
	na := image.Rect(at.X-maxPx, at.Y-maxPx, at.X+r.Dx()+maxPx, at.Y+r.Dy()+maxPx)
	nl := lumOf(native, na)
	nw := na.Dx()
	measure := func(sx, sy int) float64 {
		var sum float64
		var n int
		for y := maxPx; y < r.Dy()-maxPx; y++ {
			ia := y * r.Dx()
			ib := (y+sy+maxPx)*nw + sx + maxPx
			for x := maxPx; x < r.Dx()-maxPx; x++ {
				a, b := ra[ia+x], nl[ib+x]
				if a < 0 || b < 0 {
					continue
				}
				sum += math.Abs(a - b)
				n++
			}
		}
		if n == 0 {
			return math.Inf(1)
		}
		return sum / float64(n)
	}
	unshifted = measure(0, 0)
	best = unshifted
	for sy := -maxPx; sy <= maxPx; sy++ {
		for sx := -maxPx; sx <= maxPx; sx++ {
			if m := measure(sx, sy); m < best {
				best, dx, dy = m, sx, sy
			}
		}
	}
	return dx, dy, best, unshifted
}
