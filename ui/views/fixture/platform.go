// Package fixture is the views' platform over the simulated daemon (ui/state/scene): what a
// window shows in its fixture mode (ui/native -fixture, today's ?demo page) and what the
// views' tests render references with (viewstest.ScenePlatform).
package fixture

import (
	"context"

	"downex/ui/core/pageurl"
	"downex/ui/host"
	"downex/ui/state/scene"
	"downex/ui/views"
)

// Platform is the views.Platform of the simulated daemon (ui/state/scene): the demo's
// files, pickers, progress windows and notices (today's mock api).
type Platform struct {
	Mock *scene.Mock
	// Copied are the texts CopyText put on the clipboard.
	Copied []string
}

var _ views.Platform = (*Platform)(nil)

// OpenFile is Platform's.
func (p *Platform) OpenFile(ctx context.Context, id int64) error {
	return p.Mock.OpenFile(ctx, id)
}

// OpenWith is Platform's.
func (p *Platform) OpenWith(ctx context.Context, id int64) error {
	return p.Mock.OpenWith(ctx, id)
}

// OpenFolder is Platform's.
func (p *Platform) OpenFolder(ctx context.Context, id int64) error {
	return p.Mock.OpenFolder(ctx, id)
}

// FileExists is Platform's.
func (p *Platform) FileExists(ctx context.Context, id int64) (bool, error) {
	return p.Mock.FileExists(ctx, id)
}

// FreeSpace is Platform's.
func (p *Platform) FreeSpace(dir string) (int64, error) { return p.Mock.FreeSpace(dir) }

// OpenAppFolder is Platform's.
func (p *Platform) OpenAppFolder(_ context.Context, kind host.FolderKind) error {
	return p.Mock.OpenAppFolder(string(kind))
}

// ExtensionFolder is Platform's.
func (p *Platform) ExtensionFolder(kind host.FolderKind) (string, error) {
	return p.Mock.ExtensionFolder(string(kind))
}

// RegisterBrowsers is Platform's.
func (p *Platform) RegisterBrowsers(context.Context) error { return p.Mock.RegisterBrowsers() }

// OpenProgressWindow is Platform's.
func (p *Platform) OpenProgressWindow(id int64) error { return p.Mock.OpenProgressWindow(id) }

// PickFolder is Platform's.
func (p *Platform) PickFolder(_ context.Context, initial string) (string, error) {
	return p.Mock.PickFolder(initial), nil
}

// PickFile is Platform's.
func (p *Platform) PickFile(_ context.Context, _, initial string) (string, error) {
	return p.Mock.PickFile(initial), nil
}

// OpenExternal is Platform's.
func (p *Platform) OpenExternal(_ context.Context, url string) error {
	return p.Mock.OpenExternal(url)
}

// OpenPage is Platform's: it checks the address and opens nothing.
func (p *Platform) OpenPage(_ context.Context, url string) error {
	_, err := pageurl.Check(url)
	return err
}

// CopyText is Platform's: it records the text.
func (p *Platform) CopyText(_ context.Context, text string) error {
	p.Copied = append(p.Copied, text)
	return nil
}

// Diagnostics is Platform's.
func (p *Platform) Diagnostics(context.Context) string {
	s, err := p.Mock.Diagnostics("")
	if err != nil {
		return ""
	}
	return s
}

// ThirdPartyNotices is Platform's.
func (p *Platform) ThirdPartyNotices() string { return p.Mock.ThirdPartyNotices() }
