package tokens

import "downex/ui/kit"

// forcedRole is what a colour token becomes in forced colours (a Windows contrast theme,
// CSS forced-colors: active). The browser forces each colour by the property it paints
// (CSS Color Adjustment Level 1 §3.1): backgrounds to Canvas, text and borders to
// CanvasText, and the components' own forced-colours rules use Highlight and HighlightText
// for what is on or selected (ProgressBar's fill, the close button's hover, the focus).
// app.css's tokens are each used mostly one way; the table says which. A token missing
// from it keeps its light value, and TestForcedRoles fails, so a new token gets a role.
type forcedRole uint8

const (
	roleCanvas forcedRole = iota + 1
	roleText
	roleHighlight
	roleHighlightText
	// roleKeep: the logo and the file tiles keep their own colours, as images do
	// (FileIcon.svelte leaves forced-color-adjust at auto; unverified on a contrast theme).
	roleKeep
)

var forcedRoles = map[string]forcedRole{
	"--chrome": roleCanvas, "--surface": roleCanvas, "--surface-2": roleCanvas, "--raised": roleCanvas,
	"--field": roleCanvas, "--hover": roleCanvas, "--press": roleCanvas, "--sel": roleCanvas,
	"--sel-hover": roleCanvas, "--accent-soft": roleCanvas, "--warn-soft": roleCanvas,
	"--danger-soft": roleCanvas, "--info-soft": roleCanvas, "--seg-track": roleCanvas,
	"--chart-fill": roleCanvas, "--chart-grid": roleCanvas, "--scrim": roleCanvas, "--dim": roleCanvas,
	"--tone-on": roleCanvas,

	"--ink": roleText, "--ink-2": roleText, "--muted": roleText, "--faint": roleText,
	"--accent-text": roleText, "--warn": roleText, "--danger": roleText, "--info": roleText,
	"--line": roleText, "--line-soft": roleText, "--line-strong": roleText, "--line-field": roleText,
	"--accent-line": roleText, "--sel-line": roleText, "--warn-line": roleText, "--danger-line": roleText,
	"--info-line": roleText, "--split-line": roleText, "--seg-idle": roleText,
	"--st-downloading": roleText, "--st-queued": roleText, "--st-paused": roleText, "--st-failed": roleText,
	"--st-completed": roleText, "--st-confirm": roleText, "--st-canceled": roleText,

	"--focus": roleHighlight, "--accent": roleHighlight, "--accent-hover": roleHighlight,
	"--accent-press": roleHighlight, "--seg-fill": roleHighlight, "--seg-edge": roleHighlight,
	"--seg-paused": roleHighlight, "--seg-failed": roleHighlight, "--chart-line": roleHighlight,
	"--winctl-close": roleHighlight,

	"--accent-on": roleHighlightText, "--danger-on": roleHighlightText,

	"--tile-glyph": roleKeep, "--c-video": roleKeep, "--c-video-deep": roleKeep, "--c-audio": roleKeep,
	"--c-audio-deep": roleKeep, "--c-archives": roleKeep, "--c-archives-deep": roleKeep,
	"--c-documents": roleKeep, "--c-documents-deep": roleKeep, "--c-programs": roleKeep,
	"--c-programs-deep": roleKeep, "--c-images": roleKeep, "--c-images-deep": roleKeep,
	"--c-other": roleKeep, "--c-other-deep": roleKeep, "--brand-tile": roleKeep,
	"--brand-hairline": roleKeep, "--brand-bar": roleKeep, "--brand-head": roleKeep,
	"--brand-ring": roleKeep,
}

// Forced is the forced-colours theme for a contrast theme's colours: every colour token
// takes the system colour of its role, and there are no shadows (the browser draws none in
// forced colours). Its Dark follows the contrast theme's ground.
func Forced(sc kit.SystemColors) Theme {
	t := Light
	t.forced = true
	l := sc.Canvas
	t.Dark = 0.2126*l.R+0.7152*l.G+0.0722*l.B < 0.5
	for _, c := range colorTokens {
		f := c.field(&t)
		switch forcedRoles[c.name] {
		case roleCanvas:
			*f = sc.Canvas
		case roleText:
			*f = sc.CanvasText
		case roleHighlight:
			*f = sc.Highlight
		case roleHighlightText:
			*f = sc.HighlightText
		}
	}
	for _, s := range shadowTokens {
		*s.field(&t) = nil
	}
	return t
}

// IsForced reports whether t is the forced-colours theme.
func (t *Theme) IsForced() bool { return t.forced }
