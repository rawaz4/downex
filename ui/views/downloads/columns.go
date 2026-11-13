package downloads

import (
	"math"

	"downex/ui/core/display"
	"downex/ui/core/format"
	"downex/ui/state"
)

// The table's constants (DownloadTable.svelte).
const (
	// maxWidthRem is the widest a column may be resized to (MAX_WIDTH_REM).
	maxWidthRem = 40
	// rowPad is the rows' and header's inline padding, start and end (PAD).
	rowPad = display.RowPadStart + display.RowPadEnd
)

// headerHeight is the header row's height for a root font size (px): 30 at 16 px
// (Math.max(30, Math.round(rem × 1.875))).
func headerHeight(rem float64) float64 { return math.Max(30, format.Round(rem*1.875)) }

// Columns are the table's column widths: the saved ones (downex.columnWidths in the prefs,
// in CSS px by column key; only the resizable columns) over the defaults in rem. A failure
// to read or write the prefs leaves the defaults, or the widths for this session, as today.
type Columns struct {
	storage state.Storage
	widths  map[string]float64
}

// NewColumns reads the saved widths from s (nil: none, kept for the session only).
func NewColumns(s state.Storage) *Columns {
	return &Columns{storage: s, widths: state.ColumnWidths(s)}
}

// Saved is the width a column was resized to (px), and whether it was.
func (c *Columns) Saved(key string) (float64, bool) {
	w, ok := c.widths[key]
	return w, ok
}

// WidthOf is a column's width in px at rem px per rem: the saved one, else its default
// (widthOf; 0 for the flexible Name column, which takes the rest).
func (c *Columns) WidthOf(col display.Column, rem float64) float64 {
	if w, ok := c.widths[col.Key]; ok {
		return w
	}
	return col.Width * rem
}

// SetWidth resizes a column to px, clamped to its minimum and 40 rem and rounded (setWidth);
// the caller saves when the gesture ends. It reports whether the width changed.
func (c *Columns) SetWidth(col display.Column, px, rem float64) bool {
	if col.Width <= 0 {
		return false
	}
	w := format.Round(math.Min(maxWidthRem*rem, math.Max(col.Min*rem, px)))
	if old, ok := c.widths[col.Key]; ok && old == w {
		return false
	}
	c.widths[col.Key] = w
	return true
}

// ResizeBy resizes a column by delta px and saves the widths (resizeBy: Ctrl+Left/Right in the
// header row).
func (c *Columns) ResizeBy(col display.Column, delta, rem float64) bool {
	changed := c.SetWidth(col, c.WidthOf(col, rem)+delta, rem)
	c.Save()
	return changed
}

// Reset gives a column its default width back and saves (resetWidth: a double click on its
// handle). It reports whether it had another width.
func (c *Columns) Reset(key string) bool {
	_, had := c.widths[key]
	delete(c.widths, key)
	c.Save()
	return had
}

// Save keeps the widths in the prefs (saveWidths).
func (c *Columns) Save() { state.SaveColumnWidths(c.storage, c.widths) }

// Layout is where the shown columns sit in a row: the columns that fit, each one's start and
// width in px from the start of the row's content (after its 14 px start padding), as the
// grid template places them (`minmax(min rem, 1fr)` for Name, the others' px widths).
type Layout struct {
	Shown []display.Column
	// X and W are each shown column's start and width (px) from the content's start.
	X, W []float64
	// MinInline is the row's least width (--min-inline): the padding, Name at its minimum
	// and the others' widths. Only below it would the table scroll sideways.
	MinInline float64
	// Content is the width the columns fill (the row less its padding), at least the
	// columns' least width.
	Content float64
	Rem     float64
}

// Index is the position of a column in Shown, or -1.
func (l *Layout) Index(key string) int {
	for i, c := range l.Shown {
		if c.Key == key {
			return i
		}
	}
	return -1
}

// Fit lays the columns out in a row gridWidth px wide (the grid's client width, without its
// scroll bar): the columns that fit (fitColumns, dropping Added, Conn., Time left, Size and
// Speed in that order) and the template's tracks. A grid not measured yet (0) shows every
// column. rem is the table's measured px per rem (its script's, read once when it mounted);
// cssRem is the page's, which sizes Name's track (minmax(12rem, 1fr)). They differ only when
// the text size changed after the table mounted.
func (c *Columns) Fit(gridWidth, rem, cssRem float64) Layout {
	avail := 0.0
	if gridWidth > 0 {
		avail = gridWidth - rowPad
	}
	widthOf := func(col display.Column) float64 { return c.WidthOf(col, rem) }
	shown := display.FitColumns(avail, rem, widthOf)
	l := Layout{Shown: shown, X: make([]float64, len(shown)), W: make([]float64, len(shown)), Rem: rem}
	fixed, least := 0.0, 0.0
	for _, col := range shown {
		if col.Width == 0 {
			least += col.Min * rem
		} else {
			fixed += widthOf(col)
			least += widthOf(col)
		}
	}
	l.MinInline = rowPad + least
	content := math.Max(gridWidth-rowPad, 0)
	x := 0.0
	for i, col := range shown {
		w := widthOf(col)
		if col.Width == 0 {
			w = math.Max(col.Min*cssRem, content-fixed)
		}
		l.X[i], l.W[i] = x, w
		x += w
	}
	l.Content = x
	return l
}
