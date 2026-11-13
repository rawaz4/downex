// Package kit holds the platform-free contracts of Downex's native window and the value
// types both of their sides use. The toolkit (layout, focus, semantics, motion and the
// widgets: this package and its subpackages) is written against them, and a platform
// backend (ui/win on Windows: Win32, Direct2D, DirectWrite, later UI Automation)
// implements them. Nothing here imports a platform package, so the kit builds and is
// tested on every GOOS, with fakes of the backend's side (docs: native-ui/decision.json,
// plan steps 2 to 4).
//
// # Who implements what
//
// The backend implements, for each window, a Host and the RenderTarget and Canvas that
// draw its frames; once per process, Resources (a TextMeasurer plus paths and images) and
// the TextLayout, Path and Image values it makes; and it delivers the window's input to
// the root as Events. The kit implements Root, the content of one window, and
// SemanticTree, what that window shows to assistive technology. Tests drive the kit with
// fakes of the backend's interfaces, and a backend with fakes of Root.
//
// # Units and coordinates
//
// Every length is a DIP (device-independent pixel: 1/96 inch, the CSS px) in a float32.
// Window coordinates start at the top-left corner of the client area, which in a window
// with a custom frame includes its title bar; x grows to the right and y downwards, in
// right-to-left layouts too (mirroring is the kit's business). Scale turns DIPs into
// device pixels and rem into DIPs. Colours are sRGB with straight alpha.
//
// # One UI thread
//
// One thread per process runs the message loop of every window. Every method of these
// contracts runs on it and may be called only from it, except Host.Post, which any
// goroutine may call to run a function there. The host calls into the kit (Root methods,
// SemanticTree methods and Post functions) one call at a time from its message loop, and
// these calls never nest: what a Host method causes (the SizeEvent after Resize, the
// CloseEvent after WindowClose, Detach after Close) arrives after the call in progress has
// returned. The one exception is a modal loop that the kit itself enters through the
// system (a native message box): while it runs, the host may call Frame, Paint, FrameHit
// and the SemanticTree.
//
// # Ownership
//
// Text layouts, paths and images belong to whoever made them, who releases each with its
// Release method when done; Release is idempotent and a released value draws nothing.
// They are device-independent: a lost graphics device does not affect them, and one made
// by a backend's Resources draws on every canvas of that backend. A Canvas is valid only
// during the Paint call (or the frame) that hands it out.
//
// # Errors
//
// Failures are values that wrap one of the sentinel errors below, so callers test them
// with errors.Is; a backend's errors also name the call and the HRESULT (or the
// platform's code) that failed. Drawing methods return nothing: they skip what they
// cannot draw, and RenderTarget.EndFrame reports what went wrong in the frame.
//
// # Growing the contracts
//
// Fields and constants may be added to the types here, so literals of them name their
// fields (go vet checks it outside the package). Interface method sets do not change. A
// capability that not every backend or window has comes as an interface of its own,
// which the kit discovers with a type assertion on a Host or a Canvas. So far: hosted native
// text fields (NativeFields: the title bar's search, later the dialogs' fields) and the
// renderer's name (RendererInfo). Planned this way: popup windows for menus, Select lists,
// tooltips and the palette, a drop target for links, backdrop blur under dialogs, and
// style spans inside one text layout.
//
// The contract is doc.go, geom.go, color.go, scale.go, canvas.go, text.go, host.go,
// event.go and semantics.go; contract_test.go implements every interface outside the
// package and checks the helpers.
package kit

import "errors"

// The sentinel errors of the contracts.
var (
	// ErrDeviceLost: the graphics device was lost or reset (Direct2D's
	// D2DERR_RECREATE_TARGET), and the frame was not shown (see RenderTarget.EndFrame).
	ErrDeviceLost = errors.New("kit: graphics device lost")
	// ErrClosed: the window is closed.
	ErrClosed = errors.New("kit: window closed")
	// ErrInvalid: an argument is malformed or out of range (path data, a font size, an
	// unbalanced Push and Pop).
	ErrInvalid = errors.New("kit: invalid argument")
	// ErrUnsupported: this backend, window or semantic node cannot do it.
	ErrUnsupported = errors.New("kit: not supported")
	// ErrNoNode: the semantic node does not exist, or no longer does.
	ErrNoNode = errors.New("kit: no such semantic node")
	// ErrNotEnabled: the semantic node is disabled.
	ErrNotEnabled = errors.New("kit: semantic node disabled")
)
