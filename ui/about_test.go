package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Settings → About (docs/DESKTOP-SPEC.md): the window's build, the app's folders,
// diagnostics without private data, and the open-source notices.

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
	got := buildInfoFrom(bi, true)
	want := buildInfo{
		Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: "go1.27.0", WailsVersion: "v2.17.0",
		Commit: "0123456789abcdef0123456789abcdef01234567", CommitTime: "2026-10-29T08:00:00Z", Modified: true,
	}
	if got != want {
		t.Fatalf("buildInfoFrom = %+v, want %+v", got, want)
	}
	// No VCS stamp (the tree has no commits yet, or go test): the commit fields stay out.
	b, err := json.Marshal(buildInfoFrom(&debug.BuildInfo{GoVersion: "go1.27.0"}, true))
	if err != nil || strings.Contains(string(b), "commit") || strings.Contains(string(b), "modified") ||
		strings.Contains(string(b), "wailsVersion") {
		t.Fatalf("without a stamp = %s, %v", b, err)
	}
	if got := buildInfoFrom(nil, false); got.GoVersion != runtime.Version() || got.Version != version {
		t.Fatalf("without build info = %+v", got)
	}
}

func TestStartupHasBuildInfo(t *testing.T) {
	a, _ := newTestApp(t, newFakeDaemon(), nil)
	raw, err := a.Startup()
	if err != nil {
		t.Fatal(err)
	}
	v := decodeJSON(t, raw)
	b, ok := v["build"].(map[string]any)
	if !ok {
		t.Fatalf("Startup has no build: %s", raw)
	}
	if b["version"] != version || b["os"] != runtime.GOOS || b["arch"] != runtime.GOARCH || b["goVersion"] != runtime.Version() {
		t.Errorf("build = %v", b)
	}
	if w, _ := b["wailsVersion"].(string); !strings.HasPrefix(w, "v2.") {
		t.Errorf("wailsVersion = %v", b["wailsVersion"])
	}
}

func TestOpenAppFolder(t *testing.T) {
	root := t.TempDir()
	data, logs, tools, downloads := filepath.Join(root, "data"), filepath.Join(root, "logs"), filepath.Join(root, "tools"), filepath.Join(root, "dl")
	for _, d := range []string{data, logs, downloads} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := newFakeDaemon()
	f.status = &downexv1.StatusResponse{Version: "1", DataDir: data, LogDir: logs, ToolsDir: tools}
	f.settings = &downexv1.Settings{DefaultDir: downloads}
	a, sh := newTestApp(t, f, nil)

	for kind, want := range map[string]string{"data": data, "logs": logs, "downloads": downloads} {
		if err := a.OpenAppFolder(json.RawMessage(`{"kind":"` + kind + `"}`)); err != nil {
			t.Fatalf("OpenAppFolder(%s): %v", kind, err)
		}
		if got := sh.shown[len(sh.shown)-1]; got != "folder:"+want {
			t.Errorf("%s: shown %q, want folder:%s", kind, got, want)
		}
	}
	// The tools folder does not exist yet: nothing opens.
	n := len(sh.shown)
	err := a.OpenAppFolder(json.RawMessage(`{"kind":"tools"}`))
	if err == nil || !strings.HasPrefix(err.Error(), "failed_precondition: ") || len(sh.shown) != n {
		t.Errorf("OpenAppFolder(tools) = %v (shown %v)", err, sh.shown)
	}

	// The page names a kind, never a path: anything else is refused before anything opens.
	for _, bad := range []string{`{"kind":"C:\\Windows"}`, `{"kind":"../data"}`, `{"kind":""}`, `{}`, `null`,
		`{"kind":"data","path":"C:\\"}`, `{"kind":"Data"}`, `{"kind":1}`} {
		err := a.OpenAppFolder(json.RawMessage(bad))
		if err == nil || !strings.HasPrefix(err.Error(), "invalid_argument: ") {
			t.Errorf("OpenAppFolder(%s) = %v, want invalid_argument", bad, err)
		}
	}
	if len(sh.shown) != n {
		t.Errorf("refused requests opened %v", sh.shown[n:])
	}

	// A daemon that reports a relative folder is not trusted with it either.
	f.status = &downexv1.StatusResponse{DataDir: "data"}
	f.settings = &downexv1.Settings{DefaultDir: "relative"}
	for _, kind := range []string{"data", "downloads"} {
		if err := a.OpenAppFolder(json.RawMessage(`{"kind":"` + kind + `"}`)); err == nil && sh.shown[len(sh.shown)-1] == "folder:data" {
			t.Errorf("%s: a relative folder was opened", kind)
		}
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
	a, _ := newTestApp(t, f, nil)
	a.env = func(k string) string { return env[k] }

	raw, err := a.Diagnostics(json.RawMessage(`{"webview":"141.0.3537.71"}`))
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	text := out.Text
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
	for _, want := range []string{"Downex window " + version, "downexd 0.8.2", runtime.GOOS + "/" + runtime.GOARCH,
		"WebView2 141.0.3537.71", "Go ", "Updates supported: yes", "Update address: set",
		dataShown, logsShown,
		"yt-dlp: ready 2026.10.10 (managed, openpgp+sha256)", "deno: failed (network)",
		"completed: 1", "downloading: 1", "failed: 1", "Proxy: manual", "Site logins: 1", "Host rules: 1", "Antivirus scan: on"} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostics lack %q:\n%s", want, text)
		}
	}

	for _, bad := range []string{`{"webview":"<script>"}`, `{"webview":"1.2.3","x":1}`, `{"webview":"` + strings.Repeat("1", 40) + `"}`} {
		if _, err := a.Diagnostics(json.RawMessage(bad)); err == nil || !strings.HasPrefix(err.Error(), "invalid_argument: ") {
			t.Errorf("Diagnostics(%s) = %v, want invalid_argument", bad, err)
		}
	}
	// The daemon is down: the window's own part is still there.
	f.err = connect.NewError(connect.CodeUnavailable, errors.New("downexd is not running"))
	raw, err = a.Diagnostics(nil)
	if err != nil || !strings.Contains(string(raw), "Downex window") || !strings.Contains(string(raw), "downexd: not reachable") {
		t.Errorf("Diagnostics with the daemon down = %s, %v", raw, err)
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
		if got := redactProfile(in, "windows", get); got != want {
			t.Errorf("redactProfile(%q) = %q, want %q", in, got, want)
		}
	}
	home := map[string]string{"HOME": "/home/alice"}
	if got := redactProfile("/home/alice/.local/share/Downex", "linux", func(k string) string { return home[k] }); got != "~/.local/share/Downex" {
		t.Errorf("unix home = %q", got)
	}
	if got := redactProfile("/home/alicesmith/x", "linux", func(k string) string { return home[k] }); got != "/home/alicesmith/x" {
		t.Errorf("unix prefix of another name = %q", got)
	}
}

func TestThirdPartyNotices(t *testing.T) {
	a, _ := newTestApp(t, newFakeDaemon(), nil)
	raw, err := a.ThirdPartyNotices()
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"github.com/wailsapp/wails/v2", "connectrpc.com/connect", "google.golang.org/protobuf",
		"modernc.org/sqlite", "svelte", "yt-dlp", "Deno", "The Go Programming Language"} {
		if !strings.Contains(out.Text, want) {
			t.Errorf("the notices do not name %q", want)
		}
	}
}

// TestFileExists: the Download again dialog asks whether a download's file is on disk.
func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "here.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	completed := downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	f := newFakeDaemon(
		&downexv1.Download{Id: 1, Dir: dir, Filename: "here.bin", State: completed},
		&downexv1.Download{Id: 2, Dir: dir, Filename: "gone.bin", State: completed},
		&downexv1.Download{Id: 3, Dir: "relative", Filename: "here.bin", State: completed},
		&downexv1.Download{Id: 4, Dir: dir, State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED},
	)
	a, _ := newTestApp(t, f, nil)
	for id, want := range map[int64]string{1: `{"exists":true}`, 2: `{"exists":false}`, 3: `{"exists":false}`, 4: `{"exists":false}`} {
		raw, err := a.FileExists(json.RawMessage(`{"id":"` + strconv.FormatInt(id, 10) + `"}`))
		if err != nil || string(raw) != want {
			t.Errorf("download %d: FileExists = %s, %v; want %s", id, raw, err, want)
		}
	}
	if _, err := a.FileExists(json.RawMessage(`{"id":"9"}`)); err == nil || !strings.HasPrefix(err.Error(), "not_found: ") {
		t.Errorf("unknown download: %v", err)
	}
}
