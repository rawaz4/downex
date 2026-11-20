package confirmwin_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/kit"
	"downex/ui/state/scene"
	"downex/ui/views"
	"downex/ui/views/confirmwin"
	"downex/ui/views/viewstest"

	"google.golang.org/protobuf/proto"
)

// rig is the standalone window over the harness: its resizes applied, its close recorded.
type rig struct {
	h      *viewstest.Harness
	opened []int64 // the progress windows opened
	s      *confirmwin.Screen
	closed int
	// openedAtClose is how many progress windows were asked for when the window closed.
	openedAtClose int
	sizes         []kit.Size
}

func newRig(t *testing.T, o viewstest.Options, ids func(*viewstest.Harness) []int64) *rig {
	t.Helper()
	if o.Width == 0 {
		o.Width, o.Height = confirmwin.Width, confirmwin.Height
	}
	no := false
	o.Frameless = &no
	r := &rig{}
	o.Scene = func(so *scene.Options) { so.OnOpenProgressWindow = func(id int64) { r.opened = append(r.opened, id) } }
	r.h = viewstest.New(t, o)
	r.s = confirmwin.New(r.h.App, confirmwin.Options{
		IDs:   ids(r.h),
		Close: func() { r.closed++; r.openedAtClose = len(r.opened) },
		Resize: func(s kit.Size) {
			r.sizes = append(r.sizes, s)
			r.h.Host.Host.SizeV = s
			r.h.Window.Event(kit.SizeEvent{Size: s})
		},
	})
	t.Cleanup(r.s.Dispose)
	r.h.Show(r.s)
	r.settle()
	return r
}

func (r *rig) settle() {
	for range 4 {
		r.h.Settle()
		r.h.Advance(700 * time.Millisecond) // past the guard of a dialog that opened by itself
	}
}

func awaiting(h *viewstest.Harness) []int64 {
	var ids []int64
	for _, d := range h.Main.Downloads.Awaiting() {
		ids = append(ids, d.GetId())
	}
	slices.Sort(ids)
	return ids
}

func oldest(h *viewstest.Harness) []int64 { return awaiting(h)[:1] }

// The window shows the Confirm download dialog filling it, not the main window: no scrim, no
// close button of its own, the window fitted to it.
func TestTheWindowIsTheDialog(t *testing.T) {
	r := newRig(t, viewstest.Options{Fixture: "confirm-file"}, oldest)
	c := r.s.Current()
	if c == nil {
		t.Fatal("no dialog shown")
	}
	if !c.Filled() {
		t.Fatal("the dialog does not fill the window")
	}
	if !c.CloseButton().Style.Hidden {
		t.Fatal("the dialog shows a close button of its own: the title bar closes the window")
	}
	if len(r.sizes) == 0 {
		t.Fatal("the window was not fitted to the dialog")
	}
	got := r.h.Window.Size()
	if n := c.NaturalHeight(); got.H+0.5 < n || got.H > n+1 {
		t.Fatalf("window height %v for a dialog of %v", got.H, n)
	}
	if b := c.Card().WindowBounds(); b.Min.X != 0 || b.Min.Y != 0 || b.Max.X != got.W {
		t.Fatalf("the dialog %v does not fill the window %v", b, got)
	}
	for _, e := range []string{"Cancel download", "Download later", "Start download"} {
		if !hasButton(c.Card(), e) {
			t.Fatalf("no %q button", e)
		}
	}
}

// Start download confirms it, asks for its progress window and only then closes the window
// (nothing else waits): a process whose last window this is must not end before the progress
// window opens.
func TestStartDownloadOpensTheProgressWindowThenClosesTheWindow(t *testing.T) {
	r := newRig(t, viewstest.Options{Fixture: "confirm-file"}, oldest)
	ctx, cancel := r.h.App.CallContext()
	defer cancel()
	got, err := r.h.Mock.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st := proto.Clone(got.GetSettings()).(*downexv1.Settings)
	st.ShowProgressWindow = true
	if _, err := r.h.Mock.UpdateSettings(ctx, &downexv1.UpdateSettingsRequest{Settings: st}); err != nil {
		t.Fatal(err)
	}
	r.h.Main.LoadSettings()
	c := r.s.Current()
	id := c.Download().GetId()
	press(t, c.Card(), "Start download")
	r.settle()
	if r.closed != 1 {
		t.Fatalf("closed %d times", r.closed)
	}
	d, _ := r.h.Main.Downloads.Get(id)
	if views.IsAwaiting(d.GetState()) {
		t.Fatal("the download still awaits confirmation")
	}
	if !slices.Equal(r.opened, []int64{id}) {
		t.Fatalf("progress windows %v, want %d's", r.opened, id)
	}
	if r.openedAtClose != 1 {
		t.Fatal("the window closed before it asked for the progress window")
	}
}

// The list read before the window's request runs (a process that is already warm) does not
// close the window: it waits for the request, then shows the download.
func TestListReadyBeforeTheRequestWaits(t *testing.T) {
	r := newRig(t, viewstest.Options{Fixture: "confirm-file"}, func(*viewstest.Harness) []int64 { return nil })
	if !r.h.Main.Downloads.Ready() {
		t.Fatal("the list is not read")
	}
	if r.closed != 0 || r.s.Closed() {
		t.Fatal("the window closed before anything was asked for")
	}
	if r.s.Current() != nil {
		t.Fatal("a dialog shown before anything was asked for")
	}
	want := oldest(r.h)
	r.s.Request(want)
	r.settle()
	c := r.s.Current()
	if c == nil || c.Download().GetId() != want[0] {
		t.Fatalf("after the request: dialog %v, want download %d", c, want[0])
	}
	if r.closed != 0 {
		t.Fatal("the window closed")
	}
}

// Download later and Cancel download close the window too; neither opens a progress window.
func TestLaterAndCancelCloseTheWindow(t *testing.T) {
	for _, b := range []string{"Download later", "Cancel download"} {
		t.Run(b, func(t *testing.T) {
			r := newRig(t, viewstest.Options{Fixture: "confirm-file"}, oldest)
			press(t, r.s.Current().Card(), b)
			r.settle()
			if r.closed != 1 {
				t.Fatalf("closed %d times", r.closed)
			}
			if len(r.opened) > 0 {
				t.Fatalf("progress window opened: %v", r.opened)
			}
		})
	}
}

// Several downloads of a burst show one at a time, oldest first, in the same window; Escape
// closes the window and leaves the rest waiting.
func TestABurstShowsOneAtATimeOldestFirst(t *testing.T) {
	r := newRig(t, viewstest.Options{Fixture: "confirm-file-waiting"}, oldest)
	all := awaiting(r.h)
	if len(all) < 3 {
		t.Fatalf("awaiting %v", all)
	}
	var shown []int64
	for range 2 {
		c := r.s.Current()
		if c == nil {
			t.Fatal("nothing shown")
		}
		shown = append(shown, c.Download().GetId())
		press(t, c.Card(), "Download later")
		r.settle()
	}
	if !slices.Equal(shown, all[:2]) {
		t.Fatalf("shown %v, want %v", shown, all[:2])
	}
	if r.closed != 0 {
		t.Fatal("the window closed with a download left")
	}
	r.h.Key(kit.KeyEscape, 0)
	r.settle()
	if r.closed != 1 {
		t.Fatalf("Escape: closed %d times", r.closed)
	}
	if d, _ := r.h.Main.Downloads.Get(all[2]); !views.IsAwaiting(d.GetState()) {
		t.Fatal("the last one does not wait any more")
	}
}

// A download confirmed elsewhere (the main window) leaves the window quietly.
func TestConfirmedElsewhereClosesTheWindow(t *testing.T) {
	r := newRig(t, viewstest.Options{Fixture: "confirm-file"}, oldest)
	id := r.s.Current().Download().GetId()
	ctx, cancel := r.h.App.CallContext()
	defer cancel()
	if _, err := r.h.Main.Downloads.Confirm(ctx, &downexv1.ConfirmRequest{Id: id}); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if r.closed != 1 {
		t.Fatalf("closed %d times", r.closed)
	}
}

// Shots of the window in light and dark at 100% and 150% (DOWNEX_CONFIRMWIN_SHOTS=folder).
func TestRenderShots(t *testing.T) {
	dir := os.Getenv("DOWNEX_CONFIRMWIN_SHOTS")
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	for _, fx := range []string{"confirm-file", "confirm-video-3"} {
		for _, theme := range []string{"light", "dark"} {
			for _, scale := range []float32{1, 1.5} {
				t.Run(fmt.Sprintf("%s-%s-%v", fx, theme, scale), func(t *testing.T) {
					r := newRig(t, viewstest.Options{Fixture: fx, Theme: theme, Scale: scale, Real: true}, oldest)
					if r.s.Current() == nil {
						t.Fatal("no dialog")
					}
					r.h.Window.Blur()
					img, err := r.h.Render()
					if err != nil {
						t.Fatal(err)
					}
					if dir == "" {
						return
					}
					name := filepath.Join(dir, fmt.Sprintf("confirmwin-%s-%s-%v.png", fx, theme, scale*100))
					if err := viewstest.WritePNG(name, img); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

// button is the button with that text inside e.
func button(e *kit.Element, text string) interface{ Press() } {
	var out interface{ Press() }
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if e.Style.Hidden || out != nil {
			return
		}
		if b, ok := e.Widget().(interface {
			Press()
			Label() *kit.Label
		}); ok && b.Label().Text() == text {
			out = b
			return
		}
		for _, k := range e.Children() {
			walk(k)
		}
	}
	walk(e)
	return out
}

func press(t *testing.T, e *kit.Element, text string) {
	t.Helper()
	b := button(e, text)
	if b == nil {
		t.Fatalf("no %q button", text)
	}
	b.Press()
}

func hasButton(root *kit.Element, name string) bool { return button(root, name) != nil }
