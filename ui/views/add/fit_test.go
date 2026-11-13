package add

import (
	"fmt"
	"strings"

	"downex/ui/kit"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

// allowFit is measure-dialogs.mjs's ALLOW for these dialogs: states whose content cannot fit
// the small windows (960×608 at 125 %, 900×600) and may scroll there, in one region, in a dialog
// grown to the window's height, with the footer in view.
var allowFit = map[string]bool{
	"confirm-video-6-queue@small@20": true, "confirm-video-8@small@20": true, "confirm-video-8-queue@small@20": true,
	"confirm-video-6-fail@small@20": true, "confirm-hls-queue@small@20": true, "confirm-file-longurl-fail@small@20": true,
	"confirm-video-8-long@small@20": true, "confirm-video-6-long@small@20": true, "confirm-video-6-invalid@small@20": true,
	"confirm-video-8-all@small": true,
	"add-files-more@small":      true, "add-video-8@small": true, "add-video-8-more@small": true, "add-video-6-more@small": true,
	"add-video-6-adding@small@20": true,
}

// fitMayScroll is measure-dialogs.mjs's allowed(): the strict windows (1200×760, 1190×752,
// 1920×1080) never scroll; the small ones where ALLOW says so.
func fitMayScroll(name string, w, h int, font int) bool {
	if allowFit[fmt.Sprintf("%s@%d", name, font)] {
		return true
	}
	strict := (w == 1200 && h == 760) || (w == 1190 && h == 752) || (w == 1920 && h == 1080)
	if strict {
		return false
	}
	return allowFit[name+"@small"] || allowFit[fmt.Sprintf("%s@small@%d", name, font)]
}

// fitProblems is the fit rule's judge (measure-dialogs.mjs judge over FIT_MEASURE) for the
// native dialog: inside the window, nothing scrolling (unless allowed, then in a dialog grown
// to the room it has), the footer in view, cut text with its tooltip, no field label on two
// lines, the card above the qualities and no empty quality column.
func fitProblems(h *viewstest.Harness, m views.Modal, name string, w, hgt int, scale float32, font int) []string {
	var out []string
	card := cardOf(m)
	r := card.WindowBounds()
	win := h.Window.Size()
	if r.Min.Y < -0.5 || r.Min.X < -0.5 || r.Max.Y > win.H+0.5 || r.Max.X > win.W+0.5 {
		out = append(out, fmt.Sprintf("outside the window (%v)", r))
	}
	var body *kit.Element
	switch d := m.(type) {
	case *Add:
		body = d.Body
	case *Confirm:
		body = d.Body
	}
	pane := body.Parent()
	by := body.Size().H - pane.Size().H
	if by > 0.5 {
		if !fitMayScroll(name, w, hgt, font) {
			out = append(out, fmt.Sprintf("scrolls by %.1f px", by))
		}
		room := win.H - h.App.ModalTop() - 2*16
		if win.H <= 640 {
			room = win.H - h.App.ModalTop() - 2*8
		}
		if r.Height() < room-1 {
			out = append(out, fmt.Sprintf("scrolls in a dialog %.0f px high, not grown to the %.0f px the window has", r.Height(), room))
		}
	}
	// the footer: the card's last child, inside the card
	kids := card.Children()
	foot := kids[len(kids)-1].WindowBounds()
	if foot.Min.Y < r.Min.Y-0.5 || foot.Max.Y > r.Max.Y+0.5 {
		out = append(out, "footer hidden")
	}
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if e.Style.Hidden {
			return
		}
		if l, ok := e.Widget().(interface {
			Truncated() bool
			TooltipText() string
			Text() string
		}); ok && l.Truncated() {
			if tip := tipOf(e, l.TooltipText()); !strings.Contains(normWS(tip), normWS(l.Text())) {
				out = append(out, fmt.Sprintf("cut text without its tooltip: %q", l.Text()))
			}
		}
		for _, k := range e.Children() {
			walk(k)
		}
	}
	walk(card)
	// a field's label on two lines
	for _, t := range fieldLabels(card) {
		lh := h.Window.Environment().Scale.Rem(0.78125) * 1.4
		if t.Size().H > lh*1.5 {
			out = append(out, fmt.Sprintf("label on two lines: %q", t.Text()))
		}
	}
	// the card over the qualities, no empty quality column
	var cardArea, quality *kit.Element
	switch d := m.(type) {
	case *Add:
		if d.Found() {
			cardArea, quality = d.cardArea, d.qualityArea
		}
	case *Confirm:
		if d.qualities {
			cardArea, quality = d.cardArea, d.qualityArea
		}
	}
	if quality != nil {
		if len(quality.Children()) == 0 || quality.Size().H < 1 {
			out = append(out, "an empty quality column")
		}
		if cardArea.WindowBounds().Max.Y > quality.WindowBounds().Min.Y+1 {
			out = append(out, "the card is not above the qualities")
		}
	}
	return out
}

// tipOf is the tooltip the pointer gets over e: its own text's (a cut label shows itself
// whole), else the nearest ancestor's.
func tipOf(e *kit.Element, own string) string {
	if own != "" {
		return own
	}
	for p := e; p != nil; p = p.Parent() {
		if p.Tooltip != "" {
			return p.Tooltip
		}
	}
	return ""
}

func normWS(s string) string { return strings.Join(strings.Fields(s), " ") }

// fieldLabels are the dialog's field labels (.field > label, .label-row > label).
func fieldLabels(root *kit.Element) []*views.Text {
	var out []*views.Text
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if e.Style.Hidden {
			return
		}
		if t, ok := e.Widget().(*views.Text); ok && t.Font() == views.FontLabel {
			out = append(out, t)
		}
		for _, k := range e.Children() {
			walk(k)
		}
	}
	walk(root)
	return out
}
