//go:build !windows && !(darwin && cgo)

package tray

// Supported reports whether New can show an icon on this system: not here. macOS
// has a menu-bar icon in cgo builds only (tray_darwin.go: the Downex Agent.app
// executable); a StatusNotifierItem for Linux comes later.
func Supported() bool { return false }

// Tray is a notification-area icon; on this system there is none, and a nil *Tray
// ignores every call.
type Tray struct{}

// New returns ErrNotSupported on this system.
func New(Options) (*Tray, error) { return nil, ErrNotSupported }

// SetVisible does nothing on this system.
func (*Tray) SetVisible(bool) {}

// SetTooltip does nothing on this system.
func (*Tray) SetTooltip(string) {}

// Notify does nothing on this system.
func (*Tray) Notify(Balloon) {}

// Close does nothing on this system.
func (*Tray) Close() error { return nil }
