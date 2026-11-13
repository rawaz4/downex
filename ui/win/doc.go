//go:build windows

// Package win is Downex's native Windows backend for the platform-free contracts of
// downex/ui/kit: Direct2D drawing, DirectWrite text and WIC images from pure Go (no cgo, no
// browser engine, no extra DLLs), in Win32 windows with per-monitor DPI.
//
// # Entry points
//
//   - NewResources makes the process's kit.Resources: text layouts, paths and images that
//     every window and offscreen target of the process draws. It works without a window
//     (tests measure text with it).
//   - NewOffscreen makes a kit.RenderTarget over a WIC bitmap, for screenshot tests; its
//     EncodePNG and RGBA read the result.
//   - NewApp starts the UI thread's part (COM, DPI awareness, the window class); App.NewWindow
//     opens a window that hosts a kit.Root and implements kit.Host for it, and App.Run runs
//     the message loop until every window is closed.
//
// # Threads
//
// Everything belongs to one goroutine locked to one OS thread, the UI thread, except
// Host.Post (see kit's package doc). NewApp must be called on that thread after
// runtime.LockOSThread. Resources made without an App belong to the goroutine that uses
// them, one at a time.
//
// # Architecture
//
// Only amd64 is supported: Direct2D and DirectWrite take FLOAT arguments by value, which Go's
// windows/arm64 system calls cannot pass. On other architectures the package builds, and
// NewResources, NewOffscreen and NewApp fail with an error that wraps kit.ErrUnsupported
// and says the native window needs the x64 build.
//
// The COM bindings live in internal/d2d, internal/dwrite and internal/wic, the Win32 ones in
// internal/w32; their tests check every vtable slot and GUID against the Windows SDK headers
// (internal/sdkheader).
package win
