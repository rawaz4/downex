package kit_test

import (
	"errors"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/kittest"
)

// relayRes is the fake resources with layouts that can hold other text (kit.TextRelayouter),
// as the real backend's can; it counts the layouts it made and laid out again.
type relayRes struct {
	*kittest.Resources
	made, relaid int
	fail         bool // the next relayout fails
}

type relayLayout struct {
	kit.TextLayout // the fake's layout of the text it holds now
	r              *relayRes
	released       bool
}

func (r *relayRes) LayoutText(text string, st kit.TextStyle, o kit.TextOptions) (kit.TextLayout, error) {
	t, err := r.Resources.LayoutText(text, st, o)
	if err != nil {
		return nil, err
	}
	r.made++
	return &relayLayout{TextLayout: t, r: r}, nil
}

func (l *relayLayout) Relayout(text string, st kit.TextStyle, o kit.TextOptions) error {
	if l.released {
		return kit.ErrInvalid
	}
	l.r.relaid++
	l.TextLayout.Release()
	if l.r.fail {
		l.r.fail, l.released = false, true
		return errors.New("relayout failed")
	}
	t, err := l.r.Resources.LayoutText(text, st, o)
	if err != nil {
		l.released = true
		return err
	}
	l.TextLayout = t
	return nil
}

func (l *relayLayout) Release() {
	l.released = true
	l.TextLayout.Release()
}

// relayHost is the test host with relayRes as its resources.
type relayHost struct {
	*kittest.Host
	res *relayRes
}

func (h relayHost) Resources() kit.Resources { return h.res }

// A label whose text changes lays the new text out in the layout it has, when the backend's
// layouts can: no new layout per change (a live speed changes every tick), the same
// measurements as a new layout, and a new layout again when the font's size changes, after a
// failed relayout, and once it was released.
func TestLabelRelayoutsInPlace(t *testing.T) {
	res := &relayRes{Resources: kittest.NewResources()}
	speed := kit.NewLabel("1.2 MB/s", font13, kit.TextOptions{Truncate: kit.TruncateEnd})
	other := kit.NewLabel("Downloading", font13, kit.TextOptions{})
	now := time.Unix(1_800_000_000, 0)
	w := kit.NewWindow(box(kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(8)}, speed, other), kit.WindowOptions{
		Title: "Downex", Now: func() time.Time { return now }, Palette: func(kit.Environment) kit.Palette { return testPalette }})
	h := relayHost{Host: kittest.NewHost(kit.Size{W: 400, H: 100}, env125), res: res}
	h.Quiet = true
	w.Attach(h)
	frame := func() {
		h.RunPosted()
		w.Frame(now)
		now = now.Add(time.Second / 4)
	}
	frame()
	first := speed.TextLayout()
	if first == nil || res.made != 2 {
		t.Fatalf("first frame: layout %v, %d made", first, res.made)
	}
	for i, text := range []string{"13.4 MB/s", "980 KB/s", "2.0 MB/s"} {
		speed.SetText(text)
		frame()
		got := speed.TextLayout()
		if got != first {
			t.Fatalf("change %d: a new layout object", i)
		}
		want, _ := res.Resources.LayoutText(text, font13.Style(env125.Scale), kit.TextOptions{})
		if got.Metrics() != want.Metrics() || got.(*relayLayout).TextLayout.(*kittest.Layout).Text != text {
			t.Errorf("change %d: metrics %+v, want those of a new layout %+v", i, got.Metrics(), want.Metrics())
		}
		want.Release()
		if speed.Bounds().Width() != got.Metrics().Width {
			t.Errorf("change %d: the label is %v wide, its text %v", i, speed.Bounds().Width(), got.Metrics().Width)
		}
	}
	if res.made != 2 || res.relaid != 3 {
		t.Errorf("3 changes made %d layouts (want the first 2) and relaid %d (want 3)", res.made, res.relaid)
	}
	// The same text again changes nothing.
	speed.SetText("2.0 MB/s")
	frame()
	if res.relaid != 3 {
		t.Errorf("the same text was laid out again")
	}
	// A relayout that fails: the label makes a new layout.
	res.fail = true
	speed.SetText("4.5 MB/s")
	frame()
	if l := speed.TextLayout(); l == nil || l == first || res.made != 3 || speed.Err() != nil {
		t.Errorf("after a failed relayout: layout %v (first %v), %d made, err %v", l, first, res.made, speed.Err())
	}
	// Another font size (the Windows text size): a new layout, the old one released.
	before := speed.TextLayout().(*relayLayout)
	w.Event(kit.EnvironmentEvent{Env: kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1.25, Text: 1.5}}})
	frame()
	if speed.TextLayout() == before || !before.released || res.made != 5 {
		t.Errorf("a new text size: same layout %v, old released %v, %d made", speed.TextLayout() == before, before.released, res.made)
	}
	// Leaving the window releases every layout.
	w.Detach()
	if live := res.Resources.Live; live != 0 {
		t.Errorf("%d fake layouts live after Detach", live)
	}
}
