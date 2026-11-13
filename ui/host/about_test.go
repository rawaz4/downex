package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Settings → About (docs/DESKTOP-SPEC.md): the window's build, diagnostics without private
// data, and the open-source notices (today's ui/about_test.go).

func TestBuildInfoFrom(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.27.0",
		Deps: []*debug.Module{
			{Path: "connectrpc.com/connect", Version: "v1.19.1"},
			{Path: "github.com/wailsapp/wails/v2", Version: "v2.17.0"},
		},
		Settings: []debug.BuildSetting{
			{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
			{Key: "vcs.time", Value: "2026-10-29T08:00:00Z"}, {Key: "vcs.modified", Value: "true"},
		},
	}
	got := BuildInfoFrom("0.9.2", bi, true)
	want := BuildInfo{
		Version: "0.9.2", OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: "go1.27.0", WailsVersion: "v2.17.0",
		Commit: "0123456789abcdef0123456789abcdef01234567", CommitTime: "2026-10-29T08:00:00Z", Modified: true,
	}
	if got != want {
		t.Fatalf("BuildInfoFrom = %+v, want %+v", got, want)
	}
	// A replaced Wails module shows the replacement's version.
	bi.Deps[1].Replace = &debug.Module{Path: "../wails", Version: "v2.17.1-fork"}
	if got := BuildInfoFrom("x", bi, true); got.WailsVersion != "v2.17.1-fork" {
		t.Errorf("replaced Wails = %q", got.WailsVersion)
	}
	// No VCS stamp (the tree has no commits yet, or go test), no Wails (the native window):
	// those fields stay out of today's JSON.
	b, err := json.Marshal(BuildInfoFrom("dev", &debug.BuildInfo{GoVersion: "go1.27.0"}, true))
	if err != nil || strings.Contains(string(b), "commit") || strings.Contains(string(b), "modified") ||
		strings.Contains(string(b), "wailsVersion") {
		t.Fatalf("without a stamp = %s, %v", b, err)
	}
	if got := BuildInfoFrom("", nil, false); got.GoVersion != runtime.Version() || got.Version != "dev" {
		t.Fatalf("without build info = %+v", got)
	}
}

func TestStartupHasBuildInfo(t *testing.T) {
	s, _ := newTestSession(t, newFakeDaemon(), "--progress", "42")
	st := s.Startup()
	if st.View != (View{Progress: 42}) || st.Capabilities != (View{Progress: 42}).Capabilities() {
		t.Errorf("Startup = %+v", st)
	}
	b := st.Build
	if b.Version != "1.2.3-test" || b.OS != runtime.GOOS || b.Arch != runtime.GOARCH || b.GoVersion != runtime.Version() {
		t.Errorf("build = %+v", b)
	}
	// ui/host links no Wails: the native window's build names none.
	if b.WailsVersion != "" {
		t.Errorf("wailsVersion = %q, want none", b.WailsVersion)
	}
}

func TestDiagnosticsKeepPrivateDataOut(t *testing.T) {
	home := t.TempDir()
	local, roaming := filepath.Join(home, "AppData", "Local"), filepath.Join(home, "AppData", "Roaming")
	env := map[string]string{"USERPROFILE": home, "LOCALAPPDATA": local, "APPDATA": roaming, "HOME": home}
	const endpoint = `\\.\pipe\downex-6b1f0f0e-secret`
	f := newFakeDaemon(
		&downexv1.Download{Id: 1, Url: "https://secret.example/private.iso?token=abc", Dir: filepath.Join(home, "Downloads"),
			Filename: "private-name.iso", State: downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED},
		&downexv1.Download{Id: 2, Url: "https://www.youtube.com/watch?v=secretid", Filename: "My video.mp4",
			State: downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA},
		&downexv1.Download{Id: 3, Url: "https://x.example/a", State: downexv1.DownloadState_DOWNLOAD_STATE_FAILED, ErrorCode: "http_status"},
	)
	f.status = &downexv1.StatusResponse{Version: "0.8.2", Pid: 4242, Active: 1, Endpoint: endpoint,
		DataDir: filepath.Join(roaming, "Downex"), LogDir: filepath.Join(local, "Downex", "logs"),
		ToolsDir: filepath.Join(local, "Downex", "tools"), UpdatesSupported: true}
	f.settings = &downexv1.Settings{
		MaxConcurrentDownloads: 3, DefaultMaxConnections: 8, DefaultDir: filepath.Join(home, "Downloads"),
		UpdateFeedUrl: "https://updates.secret.example/feed.json", SiteMedia: true, ToolsChannel: "nightly",
		Proxy:         &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL, HttpProxy: "proxy.secret.example:8080", Username: "alice"},
		SiteLogins:    []*downexv1.SiteLogin{{Id: "l1", UrlPattern: "https://login.secret.example/*", Username: "bob"}},
		HostRules:     []*downexv1.HostRule{{Pattern: "*.secret-host.example", MaxConnections: 2}},
		AvScanEnabled: true, AvScanCommand: filepath.Join(home, "scanner.exe"), AvScanArgs: "--key=hunter2 [File]",
	}
	f.tools = &downexv1.ToolsStatus{Enabled: true, Ready: true, Tools: []*downexv1.ToolInfo{
		{Name: "yt-dlp", State: downexv1.ToolState_TOOL_STATE_READY, Version: "2026.10.10", Managed: true, Verified: "openpgp+sha256",
			Path: filepath.Join(local, "Downex", "tools", "yt-dlp", "yt-dlp.exe")},
		{Name: "deno", State: downexv1.ToolState_TOOL_STATE_FAILED, ErrorCode: "network", Error: "GET https://secret.example failed"},
	}}
	o := DiagnosticsOptions{Version: "0.9.2", Webview: "141.0.3537.71", Env: func(k string) string { return env[k] }}

	text := Diagnostics(callCtx(t), f, o)
	// Folders below the profile are named by its variable on Windows, by ~ elsewhere.
	dataShown, logsShown := filepath.Join("%APPDATA%", "Downex"), filepath.Join("%LOCALAPPDATA%", "Downex", "logs")
	if runtime.GOOS != "windows" {
		dataShown, logsShown = "~/AppData/Roaming/Downex", "~/AppData/Local/Downex/logs"
	}
	for _, secret := range []string{endpoint, "pipe", "secret", "token", "private", "My video", "youtube", "alice", "bob",
		"hunter2", "scanner.exe", home, "updates.", "proxy.secret", "http://", "https://"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(secret)) {
			t.Errorf("diagnostics contain %q:\n%s", secret, text)
		}
	}
	for _, want := range []string{"Downex window 0.9.2", "downexd 0.8.2", runtime.GOOS + "/" + runtime.GOARCH,
		"WebView2 141.0.3537.71", "Go ", "Updates supported: yes", "Update address: set",
		dataShown, logsShown,
		"yt-dlp: ready 2026.10.10 (managed, openpgp+sha256)", "deno: failed (network)",
		"completed: 1", "downloading: 1", "failed: 1", "Proxy: manual", "Site logins: 1", "Host rules: 1", "Antivirus scan: on",
		"Downloads: 3 (1 videos or streams); completed: 1, downloading: 1, failed: 1",
		"Settings: 3 at once, 8 connections, speed limit none, category folders off"} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostics lack %q:\n%s", want, text)
		}
	}
	if runtime.GOOS != "windows" {
		// Elsewhere the profile is "~".
		if !strings.Contains(text, "Data folder: ~") {
			t.Errorf("diagnostics without ~:\n%s", text)
		}
	}
	// The native window has no browser engine, and a progress window says what it is.
	o.Webview, o.View = "", View{Progress: 3}
	text = Diagnostics(callCtx(t), f, o)
	if strings.Contains(text, "WebView2") || !strings.Contains(text, "View: progress window") {
		t.Errorf("native progress window's diagnostics:\n%s", text)
	}

	// The daemon is down: the window's own part is still there.
	f.setErr(connect.NewError(connect.CodeUnavailable, errors.New("downexd is not running")))
	text = Diagnostics(callCtx(t), f, o)
	if !strings.Contains(text, "Downex window") || !strings.Contains(text, "downexd: not reachable (unavailable)") {
		t.Errorf("diagnostics with the daemon down:\n%s", text)
	}
}

func TestCheckBrowserVersion(t *testing.T) {
	for _, ok := range []string{"", "141.0.3537.71", "141", "1.2.3"} {
		if err := CheckBrowserVersion(ok); err != nil {
			t.Errorf("CheckBrowserVersion(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"<script>", "1.2.3.4.5", strings.Repeat("1", 40), "1.", "v141"} {
		if err := CheckBrowserVersion(bad); errText(err) != "invalid_argument: webview must be a version such as 141.0.3537.71" {
			t.Errorf("CheckBrowserVersion(%q) = %v, want invalid_argument", bad, err)
		}
	}
}

func TestRedactProfilePaths(t *testing.T) {
	env := map[string]string{
		"USERPROFILE": `C:\Users\Alice`, "LOCALAPPDATA": `C:\Users\Alice\AppData\Local`, "APPDATA": `C:\Users\Alice\AppData\Roaming`,
	}
	get := func(k string) string { return env[k] }
	for in, want := range map[string]string{
		`C:\Users\Alice\AppData\Local\Downex\logs`: `%LOCALAPPDATA%\Downex\logs`,
		`c:\users\alice\appdata\roaming\Downex`:    `%APPDATA%\Downex`,
		`C:\Users\Alice\Downloads`:                 `%USERPROFILE%\Downloads`,
		`D:\Data`:                                  `D:\Data`,
		`C:\Users\AliceSmith\Downloads`:            `C:\Users\AliceSmith\Downloads`,
	} {
		if got := RedactProfile(in, "windows", get); got != want {
			t.Errorf("RedactProfile(%q) = %q, want %q", in, got, want)
		}
	}
	home := map[string]string{"HOME": "/home/alice"}
	if got := RedactProfile("/home/alice/.local/share/Downex", "linux", func(k string) string { return home[k] }); got != "~/.local/share/Downex" {
		t.Errorf("unix home = %q", got)
	}
	if got := RedactProfile("/home/alicesmith/x", "linux", func(k string) string { return home[k] }); got != "/home/alicesmith/x" {
		t.Errorf("unix prefix of another name = %q", got)
	}
}

func TestThirdPartyNotices(t *testing.T) {
	text := ThirdPartyNotices()
	for _, want := range []string{"github.com/wailsapp/wails/v2", "connectrpc.com/connect", "google.golang.org/protobuf",
		"modernc.org/sqlite", "svelte", "yt-dlp", "Deno", "The Go Programming Language"} {
		if !strings.Contains(text, want) {
			t.Errorf("the notices do not name %q", want)
		}
	}
}

// The notices are a copy of ui/THIRD-PARTY-NOTICES.txt (go:embed cannot reach it from
// here): after "node scripts/third-party-notices.mjs" regenerates that file, copy it here.
func TestNoticesMatchTheWindow(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "THIRD-PARTY-NOTICES.txt"))
	if err != nil {
		t.Fatalf("read today's notices: %v", err)
	}
	if ThirdPartyNotices() != string(want) {
		t.Fatal("ui/host/THIRD-PARTY-NOTICES.txt differs from ui/THIRD-PARTY-NOTICES.txt: copy the regenerated file here")
	}
}
