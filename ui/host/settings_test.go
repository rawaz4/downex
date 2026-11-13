package host

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
)

// future is a field of a newer downexd that this build does not know (number 99, varint 7).
var future = protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 7)

// The window writes every field back; the unknown ones are kept (today's
// TestSettingsRoundTripKeepsEveryField).
func TestSaveSettingsKeepsEveryField(t *testing.T) {
	f := newFakeDaemon()
	stored := &downexv1.Settings{
		MaxConcurrentDownloads: 3, DefaultDir: `C:\Users\me\Downloads`, DefaultMaxConnections: 8,
		Categories: []*downexv1.Category{
			{Name: "Video", Extensions: []string{"mp4", "mkv"}},
			{Name: "Books", Extensions: []string{"epub"}, Dir: `D:\Books`},
		},
		UseCategoryFolders: true, ConfirmBrowserDownloads: true, NotifyOnComplete: true, ShowTrayIcon: true,
	}
	stored.ProtoReflect().SetUnknown(future)
	f.settings = stored

	// A draft the window built itself (as from a form): it lacks the unknown field.
	next := &downexv1.Settings{
		MaxConcurrentDownloads: 4, DefaultDir: `D:\dl`, DefaultMaxConnections: 8,
		Categories: []*downexv1.Category{
			{Name: "Video", Extensions: []string{"mp4"}},
			{Name: "Comics", Extensions: []string{"cbz", "cbr"}, Dir: `D:\Comics`},
		},
		ConfirmBrowserDownloads: true, NotifyOnFailure: true, LaunchAtLogin: true,
	}
	draft := proto.CloneOf(next)
	got, err := SaveSettings(callCtx(t), f, next)
	if err != nil {
		t.Fatal(err)
	}
	want := proto.CloneOf(draft)
	want.ProtoReflect().SetUnknown(future)
	sent := f.lastRequest(t).(*downexv1.UpdateSettingsRequest).GetSettings()
	if !proto.Equal(sent, want) {
		t.Errorf("UpdateSettings sent %v, want %v", sent, want)
	}
	if !proto.Equal(got, want) {
		t.Errorf("SaveSettings returned %v, want the stored %v", got, want)
	}
	if !proto.Equal(next, draft) || len(next.ProtoReflect().GetUnknown()) != 0 {
		t.Errorf("SaveSettings changed the caller's draft: %v", next)
	}

	// Without the current settings, nothing is replaced.
	f.setErr(connect.NewError(connect.CodeUnavailable, errors.New("downexd is not running")))
	n := f.requestCount()
	if _, err := SaveSettings(callCtx(t), f, &downexv1.Settings{MaxConcurrentDownloads: 4}); !hasPrefix(err, "unavailable: ") {
		t.Errorf("SaveSettings while unreachable = %v", err)
	}
	if f.requestCount() != n+1 { // the GetSettings only
		t.Errorf("an update reached the daemon")
	}
	if _, ok := f.lastRequest(t).(*downexv1.GetSettingsRequest); !ok {
		t.Errorf("last request %T, want the GetSettings", f.lastRequest(t))
	}
	if _, err := SaveSettings(callCtx(t), f, nil); !hasPrefix(err, "invalid_argument: ") {
		t.Errorf("SaveSettings(nil) = %v", err)
	}
}

// Fields of a newer daemon survive the replacement, nested ones included: the proxy, and
// site logins matched by id; a new login gets none (today's TestSettingsRoundTripKeepsM6Fields).
func TestSaveSettingsKeepsNestedFields(t *testing.T) {
	f := newFakeDaemon()
	stored := &downexv1.Settings{
		MaxConcurrentDownloads: 3, DefaultDir: `C:\dl`, DefaultMaxConnections: 8,
		Proxy:      &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL, HttpProxy: "proxy:8080", Username: "me", HasPassword: true},
		SiteLogins: []*downexv1.SiteLogin{{Id: "a1", UrlPattern: "https://example.com/*", Username: "u", HasPassword: true}},
	}
	stored.GetProxy().ProtoReflect().SetUnknown(future)
	stored.GetSiteLogins()[0].ProtoReflect().SetUnknown(future)
	f.settings = stored

	next := &downexv1.Settings{
		MaxConcurrentDownloads: 3, DefaultDir: `C:\dl`, DefaultMaxConnections: 8,
		UseCategoryFolders: true, ConfirmBrowserDownloads: true, NotifyOnComplete: true, NotifyOnFailure: true,
		ShowTrayIcon: true, ShowProgressWindow: true, ShowCompleteDialog: true,
		Proxy: &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_PAC, HttpProxy: "proxy:8080", SocksProxy: "socks5://s:1080",
			Bypass: "<local>;*.lan", PacUrl: "https://wpad.example/proxy.pac", Username: "me", HasPassword: true},
		HostRules: []*downexv1.HostRule{{Pattern: "*.example.com", MaxConnections: 4, SpeedLimit: 2 << 20}},
		SiteLogins: []*downexv1.SiteLogin{
			{Id: "a1", UrlPattern: "https://example.com/*", Username: "u2", HasPassword: true},
			{UrlPattern: "*.example.org", Username: "new"},
		},
		ClipboardMonitor: true, AvScanEnabled: true, AvScanCommand: `C:\Tools\scan.exe`, AvScanArgs: "--quiet [File]",
		UpdateFeedUrl: "https://updates.example/feed.json",
	}
	if _, err := SaveSettings(callCtx(t), f, next); err != nil {
		t.Fatal(err)
	}
	want := proto.CloneOf(next)
	want.GetProxy().ProtoReflect().SetUnknown(future)
	want.GetSiteLogins()[0].ProtoReflect().SetUnknown(future)
	got := f.lastRequest(t).(*downexv1.UpdateSettingsRequest).GetSettings()
	if !proto.Equal(got, want) {
		t.Errorf("UpdateSettings sent\n%v\nwant\n%v", got, want)
	}
	if u := got.GetSiteLogins()[1].ProtoReflect().GetUnknown(); len(u) != 0 {
		t.Errorf("a new site login got the unknown fields of another: %v", u)
	}
}

// A toggle is edited on the very message GetSettings returned: every field, known or not,
// goes back as it was, with the one change.
func TestEditSettingsEditsTheDaemonsMessage(t *testing.T) {
	f := newFakeDaemon()
	stored := &downexv1.Settings{
		MaxConcurrentDownloads: 3, DefaultDir: `C:\dl`, ShowCompleteDialog: true, ShowProgressWindow: true,
		Proxy: &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_SYSTEM},
	}
	stored.ProtoReflect().SetUnknown(future)
	stored.GetProxy().ProtoReflect().SetUnknown(future)
	f.settings = stored

	// "Don't show this dialog again".
	got, err := EditSettings(callCtx(t), f, func(s *downexv1.Settings) error {
		s.ShowCompleteDialog = false
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := proto.CloneOf(stored)
	want.ShowCompleteDialog = false
	if sent := f.lastRequest(t).(*downexv1.UpdateSettingsRequest).GetSettings(); !proto.Equal(sent, want) {
		t.Errorf("UpdateSettings sent %v, want %v", sent, want)
	}
	if !proto.Equal(got, want) {
		t.Errorf("EditSettings returned %v", got)
	}

	// An edit that fails writes nothing; so does a daemon that answers without settings.
	n := f.requestCount()
	stop := errors.New("not now")
	if _, err := EditSettings(callCtx(t), f, func(*downexv1.Settings) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("EditSettings with a failing edit = %v", err)
	}
	f.mu.Lock()
	f.settings = nil
	f.mu.Unlock()
	if _, err := EditSettings(callCtx(t), f, func(*downexv1.Settings) error { return nil }); !hasPrefix(err, "internal: ") {
		t.Errorf("EditSettings without settings = %v", err)
	}
	if f.requestCount() != n+2 { // two GetSettings, no update
		t.Errorf("%d requests, want only the two reads", f.requestCount()-n)
	}
	f.setErr(connect.NewError(connect.CodeUnavailable, errors.New("down")))
	if _, err := EditSettings(callCtx(t), f, func(*downexv1.Settings) error { t.Error("edited without settings"); return nil }); !hasPrefix(err, "unavailable: ") {
		t.Errorf("EditSettings while unreachable = %v", err)
	}
}

// KeepUnknown leaves everything alone when the daemon's settings have nothing unknown.
func TestKeepUnknownWithNothingUnknown(t *testing.T) {
	cur := &downexv1.Settings{MaxConcurrentDownloads: 3, SiteLogins: []*downexv1.SiteLogin{{Id: "a1"}}}
	next := &downexv1.Settings{MaxConcurrentDownloads: 5, SiteLogins: []*downexv1.SiteLogin{{Id: "a1", Username: "x"}}}
	want := proto.CloneOf(next)
	KeepUnknown(next.ProtoReflect(), cur.ProtoReflect())
	if !proto.Equal(next, want) {
		t.Errorf("KeepUnknown changed %v", next)
	}
}
