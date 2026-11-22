package confirmwin_test

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/state/scene"
	"downex/ui/views"
	"downex/ui/views/add"
	"downex/ui/views/confirmwin"
	"downex/ui/views/viewstest"

	"google.golang.org/protobuf/proto"
)

// rig is the standalone window over the harness: its resizes applied, its close recorded.
// sizes are the sizes asked for, maxes the largest sizes set and log both, in order.
type rig struct {
	h      *viewstest.Harness
	opened []int64 // the progress windows opened
	s      *confirmwin.Screen
	closed int
	// openedAtClose is how many progress windows were asked for when the window closed.
	openedAtClose int
	sizes         []kit.Size
	maxes         []kit.Size
	log           []string
}

func newRig(t *testing.T, o viewstest.Options, ids func(*viewstest.Harness) []int64) *rig {
	t.Helper()
	if o.Width == 0 {
		o.Width, o.Height = host.ConfirmWidth, host.ConfirmHeight
	}
	no := false
	o.Frameless = &no
	r := &rig{}
	scenes := o.Scene
	o.Scene = func(so *scene.Options) {
		if scenes != nil {
			scenes(so)
		}
		so.OnOpenProgressWindow = func(id int64) { r.opened = append(r.opened, id) }
	}
	r.h = viewstest.New(t, o)
	r.s = confirmwin.New(r.h.App, confirmwin.Options{
		IDs:   ids(r.h),
		Close: func() { r.closed++; r.openedAtClose = len(r.opened) },
		Resize: func(s kit.Size) {
			r.sizes = append(r.sizes, s)
			r.log = append(r.log, fmt.Sprint("resize ", s))
			r.h.Host.Host.SizeV = s
			r.h.Window.Event(kit.SizeEvent{Size: s})
		},
		MaxSize: func(s kit.Size) {
			r.maxes = append(r.maxes, s)
			r.log = append(r.log, fmt.Sprint("max ", s))
		},
	})
	t.Cleanup(r.s.Dispose)
	r.h.Show(r.s)
	r.settle()
	return r
}

// userResize is the user dragging the window's frame to size s.
func (r *rig) userResize(s kit.Size) {
	r.h.Host.Host.SizeV = s
	r.h.Window.Event(kit.SizeEvent{Size: s})
	r.settle()
}

// fitSize is the size the window fits dialog c to: its preferred width, the height its
// content asks for at that width.
func (r *rig) fitSize(c *add.Confirm) kit.Size {
	pw := c.PreferredWidth(r.h.Window.Environment().Scale)
	h, ok := c.NaturalHeightAt(pw)
	if !ok {
		r.h.T.Fatalf("the dialog was not measured at %v", pw)
	}
	return kit.Size{W: pw, H: kit.DIP(math.Ceil(float64(h)))}
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
	got := r.h.Window.Size()
	if pw := c.PreferredWidth(r.h.Window.Environment().Scale); got.W != pw {
		t.Fatalf("window width %v for a dialog %v wide", got.W, pw)
	}
	if n := c.NaturalHeight(); got.H+0.5 < n || got.H > n+1 {
		t.Fatalf("window height %v for a dialog of %v", got.H, n)
	}
	// The window opens as large as the dialog of a file asks (no resize after it shows).
	if len(r.sizes) > 0 {
		t.Errorf("the window opened at %v×%v and was resized to %v", host.ConfirmWidth, host.ConfirmHeight, r.sizes)
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

// A dialog shown is fitted in one resize, at its width and the height its content asks for
// at that width, wherever the window started; the largest size is let go before the resize
// and set after it: a quarter wider than the dialog, as high as its content.
func TestTheWindowFitsANewDialogInOneResize(t *testing.T) {
	for _, start := range []kit.Size{{W: 1000, H: 300}, {W: 500, H: 700}} {
		t.Run(fmt.Sprint(start), func(t *testing.T) {
			r := newRig(t, viewstest.Options{Fixture: "confirm-video-3", Width: int(start.W), Height: int(start.H)}, oldest)
			c := r.s.Current()
			want := r.fitSize(c)
			if len(r.sizes) != 1 || r.sizes[0] != want {
				t.Fatalf("resizes %v, want one to %v", r.sizes, want)
			}
			if got := r.h.Window.Size(); got != want {
				t.Fatalf("window %v, want %v", got, want)
			}
			if n := c.NaturalHeight(); math.Ceil(float64(n)) != float64(want.H) {
				t.Fatalf("the content asks for %v at the window's width, the window is %v high", n, want.H)
			}
			max := kit.Size{W: kit.DIP(math.Ceil(float64(want.W * 1.25))), H: want.H}
			if len(r.maxes) == 0 || r.maxes[len(r.maxes)-1] != max {
				t.Fatalf("largest sizes %v, want %v last", r.maxes, max)
			}
			if i := slices.Index(r.log, fmt.Sprint("resize ", want)); i != 0 {
				t.Fatalf("a largest size was set before the resize: %v", r.log)
			}
		})
	}
}

// The user's size is kept: a width that rewraps the dialog does not snap its height, and the
// largest height follows what the content needs at the user's width. Content that changes
// then does not resize the window either; the next dialog of the burst is fitted again.
func TestTheUserSizeIsKept(t *testing.T) {
	r := newRig(t, viewstest.Options{Fixture: "confirm-file-waiting"}, oldest)
	c := r.s.Current()
	fitted := len(r.sizes)
	for _, user := range []kit.Size{{W: 820, H: 520}, {W: 560, H: 400}} {
		r.userResize(user)
		if len(r.sizes) != fitted {
			t.Fatalf("user size %v: the window was resized to %v", user, r.sizes[fitted:])
		}
		if got := r.h.Window.Size(); got != user {
			t.Fatalf("window %v, want the user's %v", got, user)
		}
		n := c.NaturalHeight()
		if m := r.maxes[len(r.maxes)-1]; m.H != max(kit.DIP(math.Ceil(float64(n))), host.ConfirmMinHeight) {
			t.Fatalf("user size %v: largest %v for content %v high", user, m, n)
		}
	}
	grow(r, c, 48)
	if len(r.sizes) != fitted {
		t.Fatalf("content grown at the user's size: resized to %v", r.sizes[fitted:])
	}

	press(t, c.Card(), "Download later")
	r.settle()
	next := r.s.Current()
	if next == nil || next == c {
		t.Fatal("the next download is not shown")
	}
	want := r.fitSize(next)
	if len(r.sizes) != fitted+1 || r.sizes[fitted] != want {
		t.Fatalf("the next dialog: resizes %v, want one to %v", r.sizes[fitted:], want)
	}
}

// Content that grows (a note, another video) at the width the window was given fits the
// window's height again, past the largest height set before.
func TestContentChangeRefits(t *testing.T) {
	r := newRig(t, viewstest.Options{Fixture: "confirm-file"}, oldest)
	c := r.s.Current()
	before := r.h.Window.Size()
	n := len(r.sizes)
	r.log = nil
	grow(r, c, 48)
	if len(r.sizes) != n+1 {
		t.Fatalf("resizes %v after the content grew", r.sizes[n:])
	}
	got := r.sizes[n]
	if got.W != before.W || got.H < before.H+48 || got.H != kit.DIP(math.Ceil(float64(c.NaturalHeight()))) {
		t.Fatalf("resized from %v to %v for content %v high", before, got, c.NaturalHeight())
	}
	if m := r.maxes[len(r.maxes)-1]; m.H != got.H {
		t.Fatalf("largest %v after the window grew to %v", m, got)
	}
	// The largest size, which would hold the growth back, is let go first.
	if i := slices.Index(r.log, fmt.Sprint("resize ", got)); i < 1 || r.log[i-1] != fmt.Sprint("max ", kit.Size{}) {
		t.Fatalf("the largest size was not let go before the resize: %v", r.log)
	}
}

// grow adds a block h high to the dialog's body (what a note or another video adds).
func grow(r *rig, c *add.Confirm, h float32) {
	b := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, Height: kit.Px(h)}}
	c.Body.Add(b)
	c.Body.Relayout()
	r.settle()
}

// Before the dialog shows (the list and the settings are read first) the window paints the
// dialog's ground, not the kit window's --chrome; its caption is that ground too.
func TestTheWindowPaintsTheDialogGround(t *testing.T) {
	for _, tc := range []struct {
		theme string
		t     *tokens.Theme
		host  host.RGB
	}{{"light", &tokens.Light, host.RaisedLight}, {"dark", &tokens.Dark, host.RaisedDark}} {
		if f := confirmwin.CaptionFrame(tc.t); f.Background != tc.t.Raised || f.Caption != tc.t.Raised || f.Text != tc.t.Ink {
			t.Errorf("%s: caption frame %+v", tc.theme, f)
		}
		if got := kit.RGB(uint32(tc.host.R)<<16 | uint32(tc.host.G)<<8 | uint32(tc.host.B)); got != tc.t.Raised {
			t.Errorf("%s: the window opens in %+v, the dialog is %+v", tc.theme, tc.host, tc.t.Raised)
		}
		if got := (host.View{Confirm: 3}).Window().Background(tc.theme == "dark"); got != tc.host {
			t.Errorf("%s: the window's spec opens in %+v", tc.theme, got)
		}
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	r := newRig(t, viewstest.Options{Fixture: "confirm-file", Theme: "dark", Real: true}, func(*viewstest.Harness) []int64 { return nil })
	if r.s.Current() != nil {
		t.Fatal("a dialog shown before anything was asked for")
	}
	img, err := r.h.Render()
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	got := img.RGBAAt(b.Dx()/2, b.Dy()/2)
	if want := host.RaisedDark; got.R != want.R || got.G != want.G || got.B != want.B {
		t.Fatalf("the empty window paints %v, want %+v", got, want)
	}
}

// showProgressWindow turns show_progress_window on.
func (r *rig) showProgressWindow(t *testing.T) {
	t.Helper()
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
}

// Start download confirms it with progress_window and closes the window: downexd opens the
// download's progress window itself, above the browser (progress_window_opening), so this
// window opens none.
func TestStartDownloadLeavesTheProgressWindowToDownexd(t *testing.T) {
	r := newRig(t, viewstest.Options{Fixture: "confirm-file"}, oldest)
	r.showProgressWindow(t)
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
	if w := r.h.Mock.Window(); !slices.Equal(w.DaemonOpened, []int64{id}) {
		t.Fatalf("downexd opened %v, want %d's progress window", w.DaemonOpened, id)
	}
	if len(r.opened) != 0 {
		t.Fatalf("the window opened progress windows %v too", r.opened)
	}
}

// With a downexd that does not open it (an older one), Start download asks for the progress
// window itself, above the browser, and only then closes the window (nothing else waits): a
// process whose last window this is must not end before the progress window opens.
func TestStartDownloadOpensTheProgressWindowThenClosesTheWindow(t *testing.T) {
	r := newRig(t, viewstest.Options{Fixture: "confirm-file", Scene: func(so *scene.Options) { so.OldDaemon = true }}, oldest)
	r.showProgressWindow(t)
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
	if w := r.h.Mock.Window(); !slices.Equal(w.Raised, []int64{id}) || len(w.DaemonOpened) != 0 {
		t.Fatalf("raised %v, opened by downexd %v: want %d's above the browser, by this window", w.Raised, w.DaemonOpened, id)
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
	if _, _, err := r.h.Main.Downloads.Confirm(ctx, &downexv1.ConfirmRequest{Id: id}); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if r.closed != 1 {
		t.Fatalf("closed %d times", r.closed)
	}
}

// closingRig is a window over fixture whose host hears it closing (Options.Closing) and takes
// the requests that come after (Options.Reopen); log is what the window told the host, in
// order: "closing", "close", "reopen [ids]".
type closingRig struct {
	h   *viewstest.Harness
	s   *confirmwin.Screen
	log []string
}

func newClosingRig(t *testing.T, fixture string) *closingRig {
	t.Helper()
	no := false
	r := &closingRig{}
	r.h = viewstest.New(t, viewstest.Options{
		Fixture: fixture, Width: host.ConfirmWidth, Height: host.ConfirmHeight, Frameless: &no,
	})
	r.s = confirmwin.New(r.h.App, confirmwin.Options{
		IDs:     oldest(r.h),
		Closing: func() { r.log = append(r.log, "closing") },
		Close:   func() { r.log = append(r.log, "close") },
		Reopen:  func(ids []int64) { r.log = append(r.log, fmt.Sprint("reopen ", ids)) },
		Resize:  func(kit.Size) {},
		MaxSize: func(kit.Size) {},
	})
	t.Cleanup(r.s.Dispose)
	r.h.Show(r.s)
	r.settle()
	return r
}

func (r *closingRig) settle() {
	for range 4 {
		r.h.Settle()
		r.h.Advance(700 * time.Millisecond)
	}
}

// The window tells the host it is closing before it closes, once: a start that comes while
// the close is on its way then opens a new window (the host's) rather than reaching this one.
func TestTheWindowSaysItClosesBeforeItCloses(t *testing.T) {
	r := newClosingRig(t, "confirm-file")
	press(t, r.s.Current().Card(), "Download later")
	r.settle()
	if want := []string{"closing", "close"}; !slices.Equal(r.log, want) {
		t.Fatalf("the window told the host %q, want %q", r.log, want)
	}
}

// A request that reaches the window after it began to close (a start the host handed it just
// before) is not dropped: it goes to Reopen, for a new window, with the downloads still
// awaiting confirmation (a repeat once, a download confirmed meanwhile and invalid ids not),
// and this window shows nothing more.
func TestARequestAfterTheCloseGoesToANewWindow(t *testing.T) {
	r := newClosingRig(t, "confirm-file-waiting")
	all := awaiting(r.h)
	if len(all) < 3 {
		t.Fatalf("awaiting %v", all)
	}
	r.h.Key(kit.KeyEscape, 0)
	r.settle()
	if !r.s.Closed() || !slices.Equal(r.log, []string{"closing", "close"}) {
		t.Fatalf("Escape: closed %v, told %q", r.s.Closed(), r.log)
	}
	ctx, cancel := r.h.App.CallContext()
	defer cancel()
	if _, _, err := r.h.Main.Downloads.Confirm(ctx, &downexv1.ConfirmRequest{Id: all[0]}); err != nil {
		t.Fatal(err)
	}
	r.settle()
	r.s.Request([]int64{all[0], all[2], 0, all[1], all[2]})
	r.settle()
	want := []string{"closing", "close", fmt.Sprint("reopen ", []int64{all[2], all[1]})}
	if !slices.Equal(r.log, want) {
		t.Fatalf("the window told the host %q, want %q", r.log, want)
	}
	if r.s.Current() != nil {
		t.Fatal("the closed window showed a dialog")
	}
	// A download not listed yet (its event has not come) is asked for too.
	r.s.Request([]int64{1 << 40})
	if got := r.log[len(r.log)-1]; got != fmt.Sprint("reopen ", []int64{1 << 40}) {
		t.Fatalf("an unlisted download: the window told the host %q", got)
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
					shot(t, r, dir, fmt.Sprintf("confirmwin-%s-%s-%v.png", fx, theme, scale*100))
				})
			}
			// The user's sizes: as wide as the window lets them, and narrow.
			for _, user := range []kit.Size{{W: 880, H: 600}, {W: host.ConfirmMinWidth, H: host.ConfirmMinHeight}} {
				t.Run(fmt.Sprintf("%s-%s-%v", fx, theme, user), func(t *testing.T) {
					r := newRig(t, viewstest.Options{Fixture: fx, Theme: theme, Scale: 1, Real: true}, oldest)
					if r.s.Current() == nil {
						t.Fatal("no dialog")
					}
					if m := r.maxes[len(r.maxes)-1]; user.W > m.W || user.H > m.H {
						user = kit.Size{W: min(user.W, m.W), H: min(user.H, m.H)} // the system holds the drag back
					}
					r.userResize(user)
					r.h.Window.Blur()
					shot(t, r, dir, fmt.Sprintf("confirmwin-%s-%s-%vx%v.png", fx, theme, user.W, user.H))
				})
			}
		}
	}
}

func shot(t *testing.T, r *rig, dir, name string) {
	t.Helper()
	img, err := r.h.Render()
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		return
	}
	if err := viewstest.WritePNG(filepath.Join(dir, name), img); err != nil {
		t.Fatal(err)
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
