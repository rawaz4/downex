// Package fieldtest drives ui/mac's native text fields in real AppKit windows for its tests
// ([fields]): synthesized key events, input-method text, the Edit menu's actions and
// snapshots of what a window shows, its native views included. Only its tests use it; it
// builds on darwin with cgo and is empty elsewhere.
package fieldtest
