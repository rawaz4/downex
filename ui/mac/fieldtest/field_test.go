//go:build darwin && cgo

package fieldtest

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/mac"
)

// The tests open real windows: AppKit runs on the main thread, which TestMain keeps and
// runs each test's body on (onMain).

var (
	app   *mac.App
	mainQ = make(chan func())
)

func TestMain(m *testing.M) {
	runtime.LockOSThread()
	a, err := mac.NewApp(mac.AppOptions{Regular: true})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app = a
	// The fields' Cut, Copy and Paste go to a private pasteboard, never the user's.
	PrivatePasteboard()
	done := make(chan int)
	go func() { done <- m.Run() }()
	for {
		select {
		case f := <-mainQ:
			f()
		case code := <-done:
			if RealPasteboardChanged() {
				fmt.Fprintln(os.Stderr, "note: the user's pasteboard changed during the tests (another program, or a leak)")
			}
			ReleasePasteboard()
			os.Exit(code)
		}
	}
}

// onMain runs f on the main thread and waits for it.
func onMain(f func()) {
	ch := make(chan struct{})
	mainQ <- func() {
		defer close(ch)
		f()
	}
	<-ch
}

// root is a test window's content: white, with what draw paints, recording the events and
// using the keys in use.
type root struct {
	host   kit.Host
	keys   []kit.KeyEvent
	texts  []string
	moves  int
	moveAt []kit.Point
	use    map[kit.Key]bool
	onKey  func(e kit.KeyEvent) bool
	draw   func(c kit.Canvas)
	frames int
}

func (r *root) Attach(h kit.Host)                { r.host = h }
func (r *root) Detach()                          {}
func (r *root) Frame(time.Time)                  { r.frames++ }
func (r *root) FrameHit(kit.Point) kit.FramePart { return kit.FrameClient }
func (r *root) Semantics() kit.SemanticTree      { return nil }
func (r *root) Paint(c kit.Canvas, dirty kit.Rect) {
	c.FillRect(dirty, kit.RGB(0xffffff))
	if r.draw != nil {
		r.draw(c)
	}
}
func (r *root) Event(e kit.Event) bool {
	switch e := e.(type) {
	case kit.KeyEvent:
		r.keys = append(r.keys, e)
		if r.onKey != nil && r.onKey(e) {
			return true
		}
		return r.use[e.Key]
	case kit.TextEvent:
		r.texts = append(r.texts, e.Text)
	case kit.PointerEvent:
		if e.Action == kit.PointerMove {
			r.moves++
			r.moveAt = append(r.moveAt, e.Pos)
		}
	}
	return false
}

func (r *root) downs() []kit.Key {
	var out []kit.Key
	for _, e := range r.keys {
		if e.Action == kit.KeyDown {
			out = append(out, e.Key)
		}
	}
	return out
}

// record is what a field's callbacks heard.
type record struct {
	changed []string
	focus   []bool
}

func (rec *record) callbacks() kit.TextFieldCallbacks {
	return kit.TextFieldCallbacks{
		Changed: func(s string) { rec.changed = append(rec.changed, s) },
		Focused: func(in bool) { rec.focus = append(rec.focus, in) },
	}
}

var font13 = kit.TextStyle{Family: "system-ui", Size: 13}

func opts() kit.TextFieldOptions {
	return kit.TextFieldOptions{
		Style: font13, Color: kit.RGB(0x1b1f24), Background: kit.RGB(0xffffff),
		Placeholder: "File name", PlaceholderColor: kit.RGB(0x8a8f98), Name: "File name",
	}
}

var titles int

// open opens a window for r and returns it with its driver.
func open(t *testing.T, r *root, size kit.Size) (*mac.Window, *Window) {
	titles++
	title := fmt.Sprintf("fieldtest-%d", titles)
	w, err := app.NewWindow(r, mac.WindowOptions{Title: title, Size: size})
	if err != nil {
		t.Errorf("window: %v", err)
		return nil, nil
	}
	d := Find(title)
	if d == nil {
		t.Errorf("window %q not found", title)
		return w, nil
	}
	if !d.Activate() {
		t.Log("the window did not become key")
	}
	Pump(0.05)
	return w, d
}

func TestFieldKeysFocusAndEdits(t *testing.T) {
	onMain(func() {
		EditMenu()
		r := &root{use: map[kit.Key]bool{kit.KeyEscape: true, kit.KeyK: true}}
		w, d := open(t, r, kit.Size{W: 420, H: 160})
		if d == nil {
			return
		}
		defer w.Close()
		rec := &record{}
		nf, err := w.NewTextField(opts(), rec.callbacks())
		if err != nil {
			t.Errorf("NewTextField: %v", err)
			return
		}
		f := nf.(kit.FieldControl)
		nf.SetBounds(kit.R(20, 20, 300, 40))
		nf.SetText("budget-plan.xlsx")
		if got := nf.Text(); got != "budget-plan.xlsx" || len(rec.changed) != 0 {
			t.Errorf("SetText: text %q, Changed %q", got, rec.changed)
		}
		nf.Focus()
		Pump(0.05)
		if len(rec.focus) != 1 || !rec.focus[0] {
			t.Errorf("Focus: Focused %v", rec.focus)
		}
		if !strings.Contains(d.Responder(), "Editor") && !strings.Contains(d.Responder(), "TextView") {
			t.Errorf("first responder %s", d.Responder())
		}
		if info := d.EditorInfo(); !strings.Contains(info, "quotes=0 dashes=0 replace=0 correct=0 spell=0 links=0 data=0 padding=0") {
			t.Errorf("the field editor changes what is typed: %s", info)
		}
		if a, b := f.Selection(); a != 16 || b != 16 {
			t.Errorf("caret after SetText and Focus at %d-%d, want the end", a, b)
		}
		d.Type("ab")
		if got := nf.Text(); got != "budget-plan.xlsxab" {
			t.Errorf("typed: %q", got)
		}
		if n := len(rec.changed); n != 2 || rec.changed[n-1] != "budget-plan.xlsxab" {
			t.Errorf("Changed %q", rec.changed)
		}
		if got := r.downs(); len(got) != 2 || got[0] != kit.KeyA || got[1] != kit.KeyB {
			t.Errorf("the root saw %v, want A and B first", got)
		}
		if len(r.texts) != 0 {
			t.Errorf("typed text reached the root: %q", r.texts)
		}
		// Keys the root uses never reach the field; those it leaves do nothing in a
		// single-line field (Enter, Tab) and keep the focus.
		d.Press(0x35, "\x1b", "\x1b", 0) // Escape: used
		d.Press(0x28, "k", "k", 8)       // Command+K: used
		d.Press(0x24, "\r", "\r", 0)     // Enter: left
		d.Press(0x30, "\t", "\t", 0)     // Tab: left
		if got := nf.Text(); got != "budget-plan.xlsxab" {
			t.Errorf("after Escape, Command+K, Enter, Tab: %q", got)
		}
		if len(rec.focus) != 1 {
			t.Errorf("the focus moved: %v", rec.focus)
		}
		if got := r.downs(); len(got) != 6 || got[2] != kit.KeyEscape || got[3] != kit.KeyK || got[4] != kit.KeyEnter || got[5] != kit.KeyTab {
			t.Errorf("the root saw %v", got)
		}
		// Edits a widget makes: selection and an undoable replacement.
		f.SetSelection(0, 6)
		if a, b := f.Selection(); a != 0 || b != 6 {
			t.Errorf("SetSelection: %d-%d", a, b)
		}
		f.SetSelection(-5, 99)
		if a, b := f.Selection(); a != 0 || b != 18 {
			t.Errorf("SetSelection clamps: %d-%d", a, b)
		}
		f.SetSelection(0, 6)
		before := len(rec.changed)
		f.ReplaceSelection("money")
		if got := nf.Text(); got != "money-plan.xlsxab" || len(rec.changed) != before+1 {
			t.Errorf("ReplaceSelection: %q, Changed %q", got, rec.changed[before:])
		}
		// Command keys the root leaves go on to the menu: its items act on the key window's
		// first responder. When the test process is not the active application its window is
		// not key and the item finds no target: the test then sends the action as the item
		// would.
		key := d.Activate()
		d.Press(0x06, "z", "z", 8)
		if !key {
			d.Action("undo:")
		}
		if got := nf.Text(); got != "budget-plan.xlsxab" {
			t.Errorf("undo (key window %v): %q", key, got)
		}
		d.Press(0x00, "a", "a", 8)
		if !key {
			d.Action("selectAll:")
		}
		if a, b := f.Selection(); a != 0 || b != 18 {
			t.Errorf("Command+A: %d-%d", a, b)
		}
		// SetText while focused: the caret at the end, the undo cleared, no Changed.
		before = len(rec.changed)
		nf.SetText("new.bin")
		if a, b := f.Selection(); a != 7 || b != 7 || len(rec.changed) != before {
			t.Errorf("SetText while focused: caret %d-%d, Changed %q", a, b, rec.changed[before:])
		}
		d.Action("undo:")
		if got := nf.Text(); got != "new.bin" {
			t.Errorf("undo after SetText: %q", got)
		}
		nf.SelectAll()
		if a, b := f.Selection(); a != 0 || b != 7 {
			t.Errorf("SelectAll: %d-%d", a, b)
		}
		nf.Blur()
		Pump(0.05)
		if len(rec.focus) != 2 || rec.focus[1] {
			t.Errorf("Blur: Focused %v", rec.focus)
		}
		if got := d.Responder(); got != "DXView" {
			t.Errorf("after Blur the first responder is %s", got)
		}
		// A click into the field focuses it, as a press does in a browser.
		d.Click(100, 30)
		Pump(0.05)
		if len(rec.focus) != 3 || !rec.focus[2] {
			t.Errorf("click: Focused %v", rec.focus)
		}
		nf.Release()
		nf.Release()
		nf.SetText("x")
		if nf.Text() != "" {
			t.Error("a released field still answers")
		}
	})
}

func TestFieldKeysWithoutMenu(t *testing.T) {
	onMain(func() {
		NoMenu()
		defer EditMenu()
		r := &root{}
		w, d := open(t, r, kit.Size{W: 300, H: 100})
		if d == nil {
			return
		}
		defer w.Close()
		nf, _ := w.NewTextField(opts(), kit.TextFieldCallbacks{})
		nf.SetBounds(kit.R(10, 10, 250, 30))
		nf.Focus()
		Pump(0.02)
		d.Type("hello")
		d.Press(0x00, "a", "a", 8)
		if a, b := nf.(kit.FieldControl).Selection(); a != 0 || b != 5 {
			t.Errorf("Command+A without a menu: %d-%d", a, b)
		}
		count := BoardCount()
		d.Press(0x07, "x", "x", 8)
		if got := nf.Text(); got != "" {
			t.Errorf("Command+X without a menu: %q", got)
		}
		if got := BoardString(); got != "hello" || BoardCount() == count {
			t.Errorf("Command+X put %q on the (private) pasteboard", got)
		}
		d.Press(0x09, "v", "v", 8)
		d.Press(0x09, "v", "v", 8)
		if got := nf.Text(); got != "hellohello" {
			t.Errorf("Command+V without a menu: %q", got)
		}
		d.Press(0x06, "z", "z", 8)
		if got := nf.Text(); got != "hello" {
			t.Errorf("Command+Z without a menu: %q", got)
		}
	})
}

func TestFieldRulesOnEdits(t *testing.T) {
	onMain(func() {
		r := &root{}
		w, d := open(t, r, kit.Size{W: 300, H: 100})
		if d == nil {
			return
		}
		defer w.Close()
		rec := &record{}
		nf, _ := w.NewField(opts(), kit.FieldOptions{Kind: kit.FieldText, MaxLength: 5}, rec.callbacks())
		nf.SetBounds(kit.R(10, 10, 250, 30))
		nf.Focus()
		Pump(0.02)
		d.Type("abcdefg")
		if got := nf.Text(); got != "abcde" {
			t.Errorf("max length 5: %q", got)
		}
		f := nf.(kit.FieldControl)
		f.SetMaxLength(0)
		d.Insert("x\r\ny\nz")
		if got := nf.Text(); got != "abcdexyz" {
			t.Errorf("a single line takes no line breaks: %q", got)
		}
		f.SetMaxLength(9)
		d.Insert("😀😀") // a surrogate pair is never split: 8 + 2 > 9
		if got := nf.Text(); got != "abcdexyz" {
			t.Errorf("max length splits a character: %q", got)
		}
		nf.SetText("0123456789") // SetText is not limited
		if got := nf.Text(); got != "0123456789" {
			t.Errorf("SetText limited: %q", got)
		}
		// An input method's composition never reaches the root; the committed text is the
		// field's (Changed).
		keys := len(r.keys)
		f.SetMaxLength(0)
		nf.SetText("")
		if !d.SetMarked("かん") {
			t.Errorf("no text view to compose in")
		}
		d.Key(true, 0x00, "a", "a", 0) // a key while composing is the input method's
		if len(r.keys) != keys {
			t.Errorf("a key reached the root during a composition: %v", r.keys[keys:])
		}
		d.Key(false, 0x00, "a", "a", 0)
		d.SetMarked("かん")
		d.Insert("漢")
		if got := nf.Text(); got != "かんa漢" {
			t.Errorf("committed: %q", got)
		}
		if n := len(rec.changed); n == 0 || !strings.HasSuffix(rec.changed[n-1], "漢") {
			t.Errorf("Changed after the commit: %q", rec.changed)
		}
	})
}

func TestFieldPassword(t *testing.T) {
	onMain(func() {
		r := &root{}
		w, d := open(t, r, kit.Size{W: 300, H: 100})
		if d == nil {
			return
		}
		defer w.Close()
		rec := &record{}
		nf, _ := w.NewField(opts(), kit.FieldOptions{Kind: kit.FieldPassword}, rec.callbacks())
		nf.SetBounds(kit.R(10, 10, 250, 30))
		nf.Focus()
		Pump(0.05)
		d.Type("pw1")
		if got := nf.Text(); got != "pw1" {
			t.Errorf("password text %q", got)
		}
		if !strings.Contains(d.Responder(), "Secure") {
			t.Errorf("a password field is edited by %s", d.Responder())
		}
		f := nf.(kit.FieldControl)
		f.SetPassword(false)
		Pump(0.05)
		if got := nf.Text(); got != "pw1" || strings.Contains(d.Responder(), "Secure") {
			t.Errorf("plain again: %q in %s", got, d.Responder())
		}
		if len(rec.focus) != 1 {
			t.Errorf("the swap moved the focus: %v", rec.focus)
		}
		d.Type("x")
		if got := nf.Text(); got != "pw1x" {
			t.Errorf("typing after the swap: %q", got)
		}
		f.SetPassword(true)
		Pump(0.05)
		if !strings.Contains(d.Responder(), "Secure") || nf.Text() != "pw1x" {
			t.Errorf("password again: %q in %s", nf.Text(), d.Responder())
		}
	})
}

func TestFieldMultiline(t *testing.T) {
	onMain(func() {
		r := &root{}
		w, d := open(t, r, kit.Size{W: 400, H: 200})
		if d == nil {
			return
		}
		defer w.Close()
		rec := &record{}
		o := opts()
		o.Style = kit.TextStyle{Family: "ui-monospace", Size: 12, LineHeight: 1.55}
		nf, err := w.NewField(o, kit.FieldOptions{Kind: kit.FieldMultiline, LineHeight: 1.55}, rec.callbacks())
		if err != nil {
			t.Errorf("multi-line: %v", err)
			return
		}
		fc := nf.(kit.FieldControl)
		// Enter is the root's: it breaks the line with ReplaceSelection, inside its call.
		r.onKey = func(e kit.KeyEvent) bool {
			if e.Action == kit.KeyDown && e.Key == kit.KeyEnter && e.Mods == 0 {
				fc.ReplaceSelection("\r\n")
				return true
			}
			return false
		}
		nf.SetBounds(kit.R(10, 10, 380, 120))
		nf.SetText("a\r\nb\rc")
		if got := nf.Text(); got != "a\nb\nc" {
			t.Errorf("line breaks: %q", got)
		}
		nf.Focus()
		Pump(0.05)
		if len(rec.focus) != 1 || !rec.focus[0] {
			t.Errorf("Focused %v", rec.focus)
		}
		fc.SetSelection(5, 5)
		d.Press(0x24, "\r", "\r", 0)
		if got := nf.Text(); got != "a\nb\nc\n" {
			t.Errorf("Enter: %q", got)
		}
		if n := len(rec.changed); n != 1 || rec.changed[0] != "a\nb\nc\n" {
			t.Errorf("Changed after Enter: %q", rec.changed)
		}
		d.Press(0x24, "\r", "\r", 1) // Shift+Enter: the field's own line break
		d.Type("d")
		if got := nf.Text(); got != "a\nb\nc\n\nd" {
			t.Errorf("Shift+Enter: %q", got)
		}
		d.Press(0x30, "\t", "\t", 0) // Tab: never a tab character
		if got := nf.Text(); strings.Contains(got, "\t") {
			t.Errorf("Tab typed into the field: %q", got)
		}
		// Reclaim: only after the window resigned key with the field focused.
		nf.(kit.FieldReclaimer).Reclaim()
		nf.Release()
	})
}

func TestRootTextInput(t *testing.T) {
	onMain(func() {
		r := &root{}
		w, d := open(t, r, kit.Size{W: 200, H: 100})
		if d == nil {
			return
		}
		defer w.Close()
		d.Type("q")
		w.SetTextInput(kit.TextInput{Active: true, Caret: kit.R(10, 10, 11, 26)})
		d.Type("é")
		w.SetTextInput(kit.TextInput{})
		d.Type("z")
		if got := strings.Join(r.texts, ","); got != "q,é,z" {
			t.Errorf("text events %q", got)
		}
		if got := r.downs(); len(got) != 3 {
			t.Errorf("keys %v", got)
		}
	})
}

func TestFieldHoverAndClose(t *testing.T) {
	onMain(func() {
		r := &root{}
		w, d := open(t, r, kit.Size{W: 300, H: 100})
		if d == nil {
			return
		}
		nf, _ := w.NewTextField(opts(), kit.TextFieldCallbacks{})
		nf.SetBounds(kit.R(10, 10, 250, 30))
		nf.Focus()
		Pump(0.05)
		// Over the field the root still hears the pointer's moves (the box's hover), once each.
		d.Move(60, 20)
		d.Move(80, 22)
		if n, m := r.movesAt(60, 20), r.movesAt(80, 22); n != 1 || m != 1 {
			t.Errorf("the root heard %d and %d moves over the focused field, want 1 each (moves %v)", n, m, r.moveAt)
		}
		w.Close()
		Pump(0.05)
		nf.SetText("x")
		nf.Focus()
		nf.Release()
		if nf.Text() != "" {
			t.Error("a field of a closed window answers")
		}
	})
}

// ---- pixels -------------------------------------------------------------------------

// inkBox is the box of the pixels in area (device pixels) darker than the ground.
func inkBox(img image.Image, area image.Rectangle) image.Rectangle {
	var box image.Rectangle
	first := true
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if (r+g+b)/3 < 0xd000 {
				p := image.Rect(x, y, x+1, y+1)
				if first {
					box, first = p, false
				} else {
					box = box.Union(p)
				}
			}
		}
	}
	return box
}

func readPNG(t *testing.T, path string) image.Image {
	fh, err := os.Open(path)
	if err != nil {
		t.Errorf("snapshot: %v", err)
		return nil
	}
	defer fh.Close()
	img, err := png.Decode(fh)
	if err != nil {
		t.Errorf("snapshot: %v", err)
	}
	return img
}

// TestFieldDrawsAsTheKit puts a field's text, edited or not, where the kit draws the same
// text as the field's stand-in (kit.TextField.PaintStandIn): the field on one box, the
// kit's text in a second box of the same size below it.
func TestFieldDrawsAsTheKit(t *testing.T) {
	onMain(func() {
		const text = "budget-plan.xlsx"
		top := kit.R(20.5, 20.25, 300, 38.75) // a content box off the pixel grid, as a dialog's
		below := top.Add(kit.Pt(0, 60))
		r := &root{}
		r.draw = func(c kit.Canvas) {
			res := r.host.Resources()
			fm, _ := res.FontMetrics(font13)
			l, err := res.LayoutText(text, font13, kit.TextOptions{})
			if err != nil {
				return
			}
			defer l.Release()
			scale := r.host.Environment().Scale
			cr := below
			base := scale.Snap(cr.Min.Y + (cr.Height()-fm.Ascent-fm.Descent)/2 + fm.Ascent)
			c.DrawText(l, kit.Pt(cr.Min.X, base-l.Metrics().Baseline), kit.RGB(0x1b1f24))
		}
		w, d := open(t, r, kit.Size{W: 340, H: 120})
		if d == nil {
			return
		}
		defer w.Close()
		nf, _ := w.NewTextField(opts(), kit.TextFieldCallbacks{})
		nf.SetBounds(top)
		nf.SetText(text)
		w.Invalidate(kit.R(0, 0, 340, 120))
		Pump(0.1)
		dir := t.TempDir()
		ppd := float64(r.host.Environment().Scale.PixelsPerDIP)
		px := func(rc kit.Rect) image.Rectangle {
			return image.Rect(int(float64(rc.Min.X)*ppd)-4, int(float64(rc.Min.Y)*ppd)-8, int(float64(rc.Max.X)*ppd)+4, int(float64(rc.Max.Y)*ppd)+8)
		}
		shift := image.Pt(0, int(60*ppd))
		check := func(name string, tolerance int) {
			path := filepath.Join(dir, name+".png")
			if !d.Snapshot(path) {
				t.Errorf("%s: no snapshot", name)
				return
			}
			img := readPNG(t, path)
			if img == nil {
				return
			}
			want := inkBox(img, px(below)).Sub(shift)
			// The text's columns only: a key window draws the caret after the text.
			area := px(top)
			if !want.Empty() {
				area.Min.X, area.Max.X = want.Min.X, want.Max.X
			}
			got := inkBox(img, area)
			if got.Empty() || want.Empty() {
				t.Errorf("%s: no ink (field %v, kit %v)", name, got, want)
				return
			}
			dx, dy := got.Min.X-want.Min.X, got.Min.Y-want.Min.Y
			dh := got.Dy() - want.Dy()
			// How far the pixels differ, on average over the ink (0-255 per channel).
			var sum, n float64
			for y := got.Min.Y; y < got.Max.Y; y++ {
				for x := got.Min.X; x < got.Max.X; x++ {
					a, _, _, _ := img.At(x, y).RGBA()
					b, _, _, _ := img.At(x+shift.X, y+shift.Y).RGBA()
					sum += float64(abs(int(a>>8) - int(b>>8)))
					n++
				}
			}
			diff := sum / max(n, 1)
			t.Logf("%s: field ink %v, kit ink %v (device pixels), mean difference %.2f", name, got, want, diff)
			if abs(dx) > tolerance || abs(dy) > tolerance || abs(dh) > tolerance || (tolerance == 0 && diff > 0.5) {
				t.Errorf("%s: the field's text is %d,%d px off the kit's (height %+d, difference %.2f); kept %s", name, dx, dy, dh, diff, path)
			}
			keep(t, path)
		}
		check("shown", 0)
		nf.Focus()
		Pump(0.1)
		t.Logf("edited in %s", d.Responder())
		check("edited", 1)
		nf.SetText("")
		nf.Blur()
		Pump(0.1)
		path := filepath.Join(dir, "placeholder.png")
		if d.Snapshot(path) {
			if img := readPNG(t, path); img != nil && inkBox(img, px(top)).Empty() {
				t.Error("the empty field shows no placeholder")
			}
		}
	})
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// keep copies a snapshot where a failing run can be looked at.
func keep(t *testing.T, path string) {
	if dst := os.Getenv("DOWNEX_FIELD_SHOTS"); dst != "" {
		b, err := os.ReadFile(path)
		if err == nil {
			_ = os.WriteFile(filepath.Join(dst, filepath.Base(path)), b, 0o644)
		}
	}
}
