//go:build darwin && cgo

package mac

import (
	"fmt"

	"downex/ui/kit"
)

// Resources is the process's device-independent drawing resources: paths, images and the
// text engine (CoreText), which every window and offscreen target of the process draws.
// It implements kit.Resources. It belongs to one goroutine at a time (the UI thread once
// there are windows). Close releases it; layouts, paths and images it made must be
// released first, and draw nothing afterwards.
//
// CoreGraphics draws on the CPU into a target's own bitmap, so nothing here belongs to a
// device and nothing is ever lost: a target keeps only caches (gradients, shadow masks)
// that it can make again.
type Resources struct {
	text    *textEngine // started by the first layout or font metrics
	textErr error       // why the text engine could not start
	closed  bool
	arc     []kit.Point // NewPath's scratch for arcs
	// live counts what was made and not yet released (tests check for leaks).
	live struct{ layouts, paths, images int }
}

// NewResources makes the process's resources. It needs no window.
func NewResources() (*Resources, error) {
	return &Resources{}, nil
}

// engine is the text engine, started on first use; a failure is kept and returned again.
func (r *Resources) engine() (*textEngine, error) {
	if r.text != nil || r.textErr != nil {
		return r.text, r.textErr
	}
	e, err := newTextEngine()
	if err != nil {
		r.textErr = fmt.Errorf("mac: text: %w", err)
		return nil, r.textErr
	}
	r.text = e
	return e, nil
}

// LayoutText implements kit.TextMeasurer.
func (r *Resources) LayoutText(text string, style kit.TextStyle, opts kit.TextOptions) (kit.TextLayout, error) {
	if err := r.usable(); err != nil {
		return nil, err
	}
	e, err := r.engine()
	if err != nil {
		return nil, err
	}
	return e.layoutText(text, style, opts)
}

// FontMetrics implements kit.TextMeasurer.
func (r *Resources) FontMetrics(style kit.TextStyle) (kit.FontMetrics, error) {
	if err := r.usable(); err != nil {
		return kit.FontMetrics{}, err
	}
	e, err := r.engine()
	if err != nil {
		return kit.FontMetrics{}, err
	}
	return e.fontMetrics(style)
}

// Close releases everything r holds. It is idempotent.
func (r *Resources) Close() {
	if r == nil || r.closed {
		return
	}
	r.closed = true
	if r.text != nil {
		r.text.close()
		r.text = nil
	}
}

var errClosed = fmt.Errorf("mac: resources closed: %w", kit.ErrClosed)

func (r *Resources) usable() error {
	if r == nil || r.closed {
		return errClosed
	}
	return nil
}

func finite(v float32) bool { return v-v == 0 }

func finitePt(p kit.Point) bool { return finite(p.X) && finite(p.Y) }

func finiteRect(r kit.Rect) bool { return finitePt(r.Min) && finitePt(r.Max) }

func finiteRRect(r kit.RRect) bool {
	q := r.Radii
	return finiteRect(r.Rect) && finite(q.TopLeft) && finite(q.TopRight) && finite(q.BottomRight) && finite(q.BottomLeft)
}

func finiteColor(c kit.Color) bool { return finite(c.R) && finite(c.G) && finite(c.B) && finite(c.A) }

// errInvalid wraps kit.ErrInvalid with a message.
func errInvalid(format string, args ...any) error {
	return fmt.Errorf("mac: "+format+": %w", append(args, kit.ErrInvalid)...)
}

var _ kit.Resources = (*Resources)(nil)
