// Package popuptest drives AppKit for the tests of ui/mac's popups, tooltips, drops and
// dialogs ([popups]): posted mouse, wheel and key events, a made-up drag, a plain text field,
// answers to an open alert or picker, and what a window shows. Only tests import it, so
// none of this is in a product build; it works on windows by their NSWindow pointer and
// knows nothing of ui/mac. It builds on darwin with cgo and is empty elsewhere.
package popuptest
