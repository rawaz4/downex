//go:build darwin && cgo

package mac

// [mac scroll] The windows' scroll bar style: the one today's WebKit window draws, AppKit's
// overlay or legacy scroller as the system asks for it, told to the kit through
// kit.Environment.ScrollBars and followed live (kit/scrollmac.go draws them).

/*
#include "scroll_darwin.h"
*/
import "C"

import "downex/ui/kit"

var scrollObserving bool

// scrollEnvironment is the system's scroll bar style and track-click setting; the first
// call starts following their changes (main thread).
func scrollEnvironment() (kit.ScrollBarStyle, bool) {
	if !scrollObserving {
		scrollObserving = true
		C.dxScrollObserve()
	}
	style := kit.ScrollBarsMacLegacy
	if C.dxScrollOverlay() {
		style = kit.ScrollBarsMacOverlay
	}
	if scrollTestOverride != kit.ScrollBarsFluent {
		style = scrollTestOverride
	}
	return style, bool(C.dxScrollClickJumps())
}

// scrollTestOverride stands in for the system's style while it is not zero: tests only.
var scrollTestOverride kit.ScrollBarStyle

// scrollTestStyle makes the system's style s (zero: the real one again) and posts AppKit's
// change notice, which the windows follow as they do the real one: tests only.
func scrollTestStyle(s kit.ScrollBarStyle) {
	scrollTestOverride = s
	C.dxScrollTestNotify()
}

// withScrollEnvironment is env with the system's scroll bar settings.
func withScrollEnvironment(env kit.Environment) kit.Environment {
	env.ScrollBars, env.ScrollClickJumps = scrollEnvironment()
	return env
}
