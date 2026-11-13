//go:build darwin && cgo

package main

// [ax] -axdump: the window's accessibility tree as the in-process client of ui/mac reads it
// (what the system's accessibility server gives VoiceOver), with the elements kept and
// the counters, for comparing with today's WebKit window.

import (
	"fmt"
	"os"
	"strings"

	"downex/ui/mac"
)

var axDumpFile string

func writeAXDump(w *mac.Window, name string) {
	win := w.AXWindow()
	if win == nil {
		return
	}
	defer win.Release()
	var b strings.Builder
	b.WriteString(mac.AXDump(win, 14))
	if f := win.Element("AXFocusedUIElement"); f != nil {
		fmt.Fprintf(&b, "\nfocused: %s %q\n", f.String("AXRole"), f.String("AXDescription"))
		f.Release()
	}
	fmt.Fprintf(&b, "elements kept: %d\ncounters: %+v\n", w.AXElements(), mac.AXStats())
	if err := os.WriteFile(name, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "macspike: axdump:", err)
	}
}
