package shell

import (
	"math"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/views"
)

// updateBarH is the update bar's height on the window's top edge.
const updateBarH kit.DIP = 3

// UpdateBar is the thin progress line across the top edge of the main window while downexd
// downloads and installs an update (docs/M6-SPEC.md §4): the accent fill grows with the
// download, a sweep runs while the size is not known yet, and the line glows while the update
// is checked and installed. It is a layer above a dialog's scrim that takes no input, so it
// shows over Settings and the update pop-up too.
type UpdateBar struct {
	kit.Element
	fraction float64 // NaN: not known
	eased    kit.Eased
	anim     *updateBarAnim
}

// NewUpdateBar makes the bar's layer.
func NewUpdateBar() *UpdateBar {
	b := &UpdateBar{fraction: math.NaN()}
	b.PassThrough = true
	b.Style = kit.Style{Display: kit.DisplayBlock}
	b.Semantics.Role = kit.RoleProgressBar
	b.Semantics.Name = "Updating Downex"
	b.eased.OnChange = b.Invalidate
	return b
}

// Set shows fraction (0 to 1, NaN while the size is not known).
func (b *UpdateBar) Set(fraction float64) {
	if fraction == b.fraction || (math.IsNaN(fraction) && math.IsNaN(b.fraction)) {
		return
	}
	b.fraction = fraction
	if math.IsNaN(fraction) {
		b.eased.Reset(float32(math.NaN()))
	} else {
		b.eased.SetTarget(b.Window(), float32(fraction))
	}
	b.Invalidate()
}

// Attached is kit.Attacher's: the sweep runs while the bar shows.
func (b *UpdateBar) Attached() {
	if w := b.Window(); w != nil && w.MotionOK() {
		b.anim = &updateBarAnim{b: b}
		w.Animate(b.anim)
	}
}

// Detached is kit.Detacher's.
func (b *UpdateBar) Detached() {
	if b.anim != nil {
		b.anim.dead = true
		b.anim = nil
	}
}

// ContentWidths is kit.Layouter's (the layer fills the window).
func (b *UpdateBar) ContentWidths(*kit.LayoutContext) (lo, hi kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (b *UpdateBar) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's: nothing inside.
func (b *UpdateBar) LayoutContent(*kit.LayoutContext, kit.Rect) {}

// Paint is kit.Painter's: a faint track, the accent fill and its moving sheen.
func (b *UpdateBar) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&b.Element)
	box := kit.RectAt(p.Origin, p.Size())
	h := p.Scale.SnapWidth(float32(updateBarH))
	track := kit.R(box.Min.X, box.Min.Y, box.Max.X, box.Min.Y+h)
	p.Canvas.FillRect(track, t.AccentSoft)
	w := track.Width()
	phase := float32(0)
	if b.anim != nil {
		phase = b.anim.phase()
	}
	v := b.eased.Value()
	if math.IsNaN(float64(v)) {
		// Not known yet: a third of the width sweeps across.
		seg := w / 3
		x := track.Min.X - seg + (w+seg)*phase
		p.Canvas.FillRect(kit.R(max(x, track.Min.X), track.Min.Y, min(x+seg, track.Max.X), track.Max.Y), t.Accent)
		return
	}
	fill := kit.R(track.Min.X, track.Min.Y, track.Min.X+w*min(1, max(0, v)), track.Max.Y)
	p.Canvas.FillRect(fill, t.Accent)
	// A short highlight runs along the fill, so that a pause in the download still looks alive.
	if fw := fill.Width(); fw > 24 && b.anim != nil {
		sw := min(fw/4, 120)
		x := fill.Min.X - sw + (fw+sw)*phase
		p.Canvas.FillRect(kit.R(max(x, fill.Min.X), fill.Min.Y, min(x+sw, fill.Max.X), fill.Max.Y), kit.Color{R: 1, G: 1, B: 1, A: 0.35})
	}
}

// updateBarAnim redraws the bar while it shows (the sweep and the sheen).
type updateBarAnim struct {
	b     *UpdateBar
	start time.Time
	now   time.Time
	dead  bool
}

// Step is kit.Animation's.
func (a *updateBarAnim) Step(now time.Time) bool {
	if a.dead {
		return false
	}
	if a.start.IsZero() {
		a.start = now
	}
	a.now = now
	a.b.Invalidate()
	return true
}

// Finish is kit.Animation's.
func (a *updateBarAnim) Finish() { a.dead = true }

func (a *updateBarAnim) phase() float32 {
	const period = 1400 * time.Millisecond
	if a.start.IsZero() {
		return 0
	}
	return float32(a.now.Sub(a.start)%period) / float32(period)
}

// ShowUpdateProgress shows the update bar while p is a running install's state (nil hides it).
func (s *Shell) ShowUpdateProgress(p *downexv1.UpdateInfo, fraction float64) {
	on := p != nil && state.UpdateInstalling(p.GetInstallStage())
	a := s.app
	switch {
	case on && s.updateBar == nil:
		s.updateBar = NewUpdateBar()
		s.updateBar.Set(fraction)
		a.Layers.Set(s.updateBar, views.ZPalette+1)
	case on:
		s.updateBar.Set(fraction)
	case s.updateBar != nil:
		a.Layers.Remove(s.updateBar)
		s.updateBar = nil
	}
}

// UpdateBarShown is the update bar while it shows (nil otherwise; tests).
func (s *Shell) UpdateBarShown() *UpdateBar { return s.updateBar }
