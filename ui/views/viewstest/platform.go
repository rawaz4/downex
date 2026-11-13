package viewstest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"downex/ui/host"
	"downex/ui/views"
	"downex/ui/views/fixture"
)

// ScenePlatform is the views.Platform of the simulated daemon (ui/state/scene): the demo's
// files, pickers, progress windows and notices (today's mock api). It lives in
// views/fixture, which a window's fixture mode uses too.
type ScenePlatform = fixture.Platform

// FakePlatform is a views.Platform that records its calls (a harness over a statetest
// daemon): files exist unless Gone says otherwise, pickers answer Picked, and calls fail with
// Err when it is set.
type FakePlatform struct {
	mu sync.Mutex
	// Calls are the calls made, by name ("OpenFile 3").
	Calls []string
	// Gone are the downloads whose files are no longer there.
	Gone map[int64]bool
	// Picked is what the pickers answer ("" cancelled).
	Picked string
	// Free is the free space FreeSpace reports.
	Free int64
	// Err fails every call that can fail.
	Err    error
	Copied []string
	// Extension is the folder ExtensionFolder answers the extension's folders in.
	Extension string
}

var _ views.Platform = (*FakePlatform)(nil)

func (p *FakePlatform) record(name string, args ...any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Calls = append(p.Calls, strings.TrimSpace(name+" "+fmt.Sprint(args...)))
	return p.Err
}

// OpenFile is Platform's.
func (p *FakePlatform) OpenFile(_ context.Context, id int64) error { return p.record("OpenFile", id) }

// OpenWith is Platform's.
func (p *FakePlatform) OpenWith(_ context.Context, id int64) error { return p.record("OpenWith", id) }

// OpenFolder is Platform's.
func (p *FakePlatform) OpenFolder(_ context.Context, id int64) error {
	return p.record("OpenFolder", id)
}

// FileExists is Platform's.
func (p *FakePlatform) FileExists(_ context.Context, id int64) (bool, error) {
	err := p.record("FileExists", id)
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.Gone[id], err
}

// FreeSpace is Platform's.
func (p *FakePlatform) FreeSpace(dir string) (int64, error) {
	err := p.record("FreeSpace", dir)
	return p.Free, err
}

// OpenAppFolder is Platform's.
func (p *FakePlatform) OpenAppFolder(_ context.Context, kind host.FolderKind) error {
	return p.record("OpenAppFolder", kind)
}

// ExtensionFolder is Platform's: the extension's folder under Extension (or
// C:\Program Files\Downex\extension); it never fails.
func (p *FakePlatform) ExtensionFolder(kind host.FolderKind) (string, error) {
	root := p.Extension
	if root == "" {
		root = `C:\Program Files\Downex\extension`
	}
	target := "chrome-mv3"
	if kind == host.FolderExtensionFirefox {
		target = "firefox-mv3"
	}
	return root + `\` + target, nil
}

// RegisterBrowsers is Platform's.
func (p *FakePlatform) RegisterBrowsers(context.Context) error { return p.record("RegisterBrowsers") }

// OpenProgressWindow is Platform's.
func (p *FakePlatform) OpenProgressWindow(id int64) error { return p.record("OpenProgressWindow", id) }

// PickFolder is Platform's.
func (p *FakePlatform) PickFolder(_ context.Context, initial string) (string, error) {
	err := p.record("PickFolder", initial)
	return p.Picked, err
}

// PickFile is Platform's.
func (p *FakePlatform) PickFile(_ context.Context, title, initial string) (string, error) {
	err := p.record("PickFile", title, initial)
	return p.Picked, err
}

// OpenExternal is Platform's.
func (p *FakePlatform) OpenExternal(_ context.Context, url string) error {
	return p.record("OpenExternal", url)
}

// CopyText is Platform's.
func (p *FakePlatform) CopyText(_ context.Context, text string) error {
	err := p.record("CopyText")
	p.mu.Lock()
	p.Copied = append(p.Copied, text)
	p.mu.Unlock()
	return err
}

// Diagnostics is Platform's.
func (p *FakePlatform) Diagnostics(context.Context) string { return "" }

// ThirdPartyNotices is Platform's.
func (p *FakePlatform) ThirdPartyNotices() string { return "" }
