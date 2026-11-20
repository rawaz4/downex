package views

import (
	"context"

	"connectrpc.com/connect"

	"downex/ui/host"
	"downex/ui/kit"
)

// Pickers are the system's dialogs a backend gives (ui/win: IFileOpenDialog for folders and
// files; the clipboard). Each runs off the UI thread and blocks until the user chose; nil
// fields answer unimplemented.
type Pickers struct {
	// PickFolder shows the folder picker titled host.FolderPickerTitle starting in start
	// (host.ExistingDir of the initial folder); "" is a cancelled picker.
	PickFolder func(ctx context.Context, title, start string) (string, error)
	// PickFile shows the file picker titled title, starting in start.
	PickFile func(ctx context.Context, title, start string) (string, error)
	// OpenExternal opens an https address in the system's browser.
	OpenExternal func(ctx context.Context, url string) error
	// OpenPage opens a download's page, an http or https address (pageurl.Check), in the
	// system's browser (docs/DESKTOP-SPEC.md §10).
	OpenPage func(ctx context.Context, url string) error
	// CopyText puts text on the clipboard.
	CopyText func(ctx context.Context, text string) error
}

// HostPlatform is the Platform of a window process: host.Session's files, progress windows
// and diagnostics, and the backend's pickers.
type HostPlatform struct {
	Session *host.Session
	Pickers Pickers
}

var _ Platform = (*HostPlatform)(nil)

func unimplemented(what string) error {
	return connect.NewError(connect.CodeUnimplemented, errString(what+" is not available here"))
}

type errString string

func (e errString) Error() string { return string(e) }

// OpenFile is Platform's.
func (p *HostPlatform) OpenFile(ctx context.Context, id int64) error {
	return p.Session.Files().OpenFile(ctx, id)
}

// OpenWith is Platform's.
func (p *HostPlatform) OpenWith(ctx context.Context, id int64) error {
	return p.Session.Files().OpenWith(ctx, id)
}

// OpenFolder is Platform's.
func (p *HostPlatform) OpenFolder(ctx context.Context, id int64) error {
	return p.Session.Files().OpenFolder(ctx, id)
}

// FileExists is Platform's.
func (p *HostPlatform) FileExists(ctx context.Context, id int64) (bool, error) {
	return p.Session.Files().FileExists(ctx, id)
}

// FreeSpace is Platform's.
func (p *HostPlatform) FreeSpace(dir string) (int64, error) { return p.Session.Files().FreeSpace(dir) }

// OpenAppFolder is Platform's.
func (p *HostPlatform) OpenAppFolder(ctx context.Context, kind host.FolderKind) error {
	return p.Session.Files().OpenAppFolder(ctx, kind)
}

// ExtensionFolder is Platform's.
func (p *HostPlatform) ExtensionFolder(kind host.FolderKind) (string, error) {
	return p.Session.Files().ExtensionFolder(kind)
}

// RegisterBrowsers is Platform's.
func (p *HostPlatform) RegisterBrowsers(ctx context.Context) error {
	return p.Session.Files().RegisterBrowsers(ctx)
}

// OpenProgressWindow is Platform's.
func (p *HostPlatform) OpenProgressWindow(id int64) error { return p.Session.OpenProgressWindow(id) }

// PickFolder is Platform's.
func (p *HostPlatform) PickFolder(ctx context.Context, initial string) (string, error) {
	if p.Pickers.PickFolder == nil {
		return "", unimplemented("the folder picker")
	}
	return p.Pickers.PickFolder(ctx, host.FolderPickerTitle, host.ExistingDir(initial))
}

// PickFile is Platform's.
func (p *HostPlatform) PickFile(ctx context.Context, title, initial string) (string, error) {
	if p.Pickers.PickFile == nil {
		return "", unimplemented("the file picker")
	}
	return p.Pickers.PickFile(ctx, host.FilePickerTitleOf(title), host.ExistingDir(initial))
}

// OpenExternal is Platform's.
func (p *HostPlatform) OpenExternal(ctx context.Context, url string) error {
	if p.Pickers.OpenExternal == nil {
		return unimplemented("opening the browser")
	}
	return p.Pickers.OpenExternal(ctx, url)
}

// OpenPage is Platform's.
func (p *HostPlatform) OpenPage(ctx context.Context, url string) error {
	if p.Pickers.OpenPage == nil {
		return unimplemented("opening the browser")
	}
	return p.Pickers.OpenPage(ctx, url)
}

// CopyText is Platform's.
func (p *HostPlatform) CopyText(ctx context.Context, text string) error {
	if p.Pickers.CopyText == nil {
		return unimplemented("the clipboard")
	}
	return p.Pickers.CopyText(ctx, text)
}

// Diagnostics is Platform's: the native window has no WebView version to report.
func (p *HostPlatform) Diagnostics(ctx context.Context) string { return p.Session.Diagnostics(ctx, "") }

// ThirdPartyNotices is Platform's.
func (p *HostPlatform) ThirdPartyNotices() string { return host.ThirdPartyNotices() }

// WindowClipboard is a Pickers.CopyText through a kit window's host (Host.SetClipboardText on
// the UI thread).
func WindowClipboard(w *kit.Window) func(ctx context.Context, text string) error {
	return func(ctx context.Context, text string) error {
		done := make(chan error, 1)
		if err := w.Post(func() {
			h := w.Host()
			if h == nil {
				done <- kit.ErrClosed
				return
			}
			done <- h.SetClipboardText(text)
		}); err != nil {
			return err
		}
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
