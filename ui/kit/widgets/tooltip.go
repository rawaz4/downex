package widgets

import (
	"downex/ui/kit"
)

// EnableTooltips turns on w's tooltips: the system's, as the WebView window's title tooltips
// are, shown where the pointer rests for the system's hover time over an element with a
// Tooltip, or over a label whose text an ellipsis or a line clamp cuts (the whole text:
// kit.Window.TooltipAt, the fit rule's truncation detection). It reports whether the host
// shows tooltips (kit.Tooltips).
func EnableTooltips(w *kit.Window) bool {
	h := w.Host()
	t, ok := h.(kit.Tooltips)
	if !ok {
		return false
	}
	t.SetTooltipSource(w.TooltipAt)
	return true
}
