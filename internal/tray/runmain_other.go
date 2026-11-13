//go:build !(darwin && cgo)

package tray

// RunMain runs f and returns its result. Only the macOS menu-bar icon (a cgo build,
// tray_darwin.go) needs the main thread; everywhere else this is just f().
func RunMain(f func() int) int { return f() }
