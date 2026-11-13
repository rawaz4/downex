//go:build darwin && cgo

package mac

// [mac webkit] Bidi isolates as WebKit sets them.

import (
	"math"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// WebKit shapes an isolate (U+2066…U+2069) as a run of its own, so no kerning pair spans its
// edges: “ and n are not kerned in “⁨novel-draft.epub⁩” (RestartDialog's quote). WKWebView on
// macOS 26 gives the span 213.1733 px at 13 px system-ui; one Core Text line kerns across
// the controls and gives 212.7925.
func TestIsolatesBreakKerning(t *testing.T) {
	e := newEngine(t)
	if segoeInstalled(e) {
		t.Skip("the system has Segoe UI")
	}
	l := lay(t, e, "Download “⁨novel-draft.epub⁩” again", kit.TextStyle{Family: tokens.FontUI, Size: 13}, kit.TextOptions{})
	if w := l.Metrics().Width; math.Abs(float64(w)-213.1733) > 0.002 {
		t.Errorf("width %.4f, WebKit's 213.1733", w)
	}
	// without isolates the pairs still kern
	l = lay(t, e, "Download “novel-draft.epub” again", kit.TextStyle{Family: tokens.FontUI, Size: 13}, kit.TextOptions{})
	if w := l.Metrics().Width; math.Abs(float64(w)-212.7925) > 0.002 {
		t.Errorf("plain width %.4f, want the kerned 212.7925", w)
	}
}
