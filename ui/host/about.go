package host

import (
	"cmp"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Settings → About (today's ui/about.go): what this window is built from, diagnostics to
// copy into a report without private data, and the open-source notices.

// BuildInfo is a window's build. The commit fields are present only when the build was
// stamped from a version-controlled tree.
type BuildInfo struct {
	Version      string `json:"version"` // the build's version ("dev" for a development build)
	OS           string `json:"os"`      // runtime.GOOS
	Arch         string `json:"arch"`    // runtime.GOARCH
	GoVersion    string `json:"goVersion"`
	WailsVersion string `json:"wailsVersion,omitempty"` // "" for a window built without Wails
	Commit       string `json:"commit,omitempty"`       // vcs.revision
	CommitTime   string `json:"commitTime,omitempty"`   // vcs.time, RFC 3339
	Modified     bool   `json:"modified,omitempty"`     // vcs.modified: built with uncommitted changes
}

// wailsModule is the Wails module whose version the About page shows when it is linked.
const wailsModule = "github.com/wailsapp/wails/v2"

// CurrentBuild is this process's build; version is its main package's version ("dev"
// when empty).
func CurrentBuild(version string) BuildInfo {
	bi, ok := debug.ReadBuildInfo()
	return BuildInfoFrom(version, bi, ok)
}

// BuildInfoFrom reads bi (ok false: no build information, the Go version is then the
// runtime's).
func BuildInfoFrom(version string, bi *debug.BuildInfo, ok bool) BuildInfo {
	b := BuildInfo{Version: cmp.Or(version, "dev"), OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version()}
	if !ok || bi == nil {
		return b
	}
	if bi.GoVersion != "" {
		b.GoVersion = bi.GoVersion
	}
	for _, m := range bi.Deps {
		if m != nil && m.Path == wailsModule {
			if m.Replace != nil && m.Replace.Version != "" {
				b.WailsVersion = m.Replace.Version
			} else {
				b.WailsVersion = m.Version
			}
		}
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			b.Commit = s.Value
		case "vcs.time":
			b.CommitTime = s.Value
		case "vcs.modified":
			b.Modified = s.Value == "true"
		}
	}
	return b
}

// ---- diagnostics ----------------------------------------------------------------------

// DiagnosticsOptions is what Diagnostics knows of the window.
type DiagnosticsOptions struct {
	// Version is the window's build version ("dev" when empty).
	Version string
	// View is what the window shows; a progress window says so.
	View View
	// Webview is the browser engine's version of a window that has one (today's WebView2
	// window); "" for the native window. CheckBrowserVersion it first.
	Webview string
	// Env reads the environment (os.Getenv when nil): the profile folders that are hidden.
	Env func(string) string
	// GOOS is the system whose profile rules apply (runtime.GOOS when "").
	GOOS string
}

var browserVersion = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,3}$`)

// CheckBrowserVersion accepts "" and a version of digits and dots, at most 32 bytes (such
// as 141.0.3537.71); anything else is invalid_argument.
func CheckBrowserVersion(v string) error {
	if v != "" && (len(v) > 32 || !browserVersion.MatchString(v)) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("webview must be a version such as 141.0.3537.71"))
	}
	return nil
}

// Diagnostics is a plain-text summary to paste into a problem report (Settings → About,
// "Copy diagnostics"): the window's build and system, the browser engine's version when the
// window has one, downexd's version, state and folders, whether updates can be installed,
// yt-dlp and Deno (state, version, how they were checked, error codes), the settings that
// change behaviour, and how many downloads are in each state. It never contains the IPC
// endpoint, an address, a file name, a cookie, a site login, a proxy or its credentials,
// the update address or the antivirus command; folders inside the user's profile are
// written as %USERPROFILE%, %APPDATA% and %LOCALAPPDATA% (elsewhere "~"). What downexd
// cannot answer is said so. ctx bounds the daemon calls (CallTimeout).
func Diagnostics(ctx context.Context, d Daemon, o DiagnosticsOptions) string {
	env := o.Env
	if env == nil {
		env = getenv
	}
	goos := cmp.Or(o.GOOS, runtime.GOOS)
	var w strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&w, format+"\n", args...) }
	path := func(p string) string {
		if p == "" {
			return "not known"
		}
		return RedactProfile(p, goos, env)
	}
	yes := func(v bool) string {
		if v {
			return "yes"
		}
		return "no"
	}
	on := func(v bool) string {
		if v {
			return "on"
		}
		return "off"
	}

	b := CurrentBuild(o.Version)
	line("Downex window %s (%s/%s, Go %s)", b.Version, b.OS, b.Arch, strings.TrimPrefix(b.GoVersion, "go"))
	if b.WailsVersion != "" {
		line("Wails %s", b.WailsVersion)
	}
	if b.Commit != "" {
		modified := ""
		if b.Modified {
			modified = ", modified"
		}
		line("Commit %s (%s%s)", b.Commit, b.CommitTime, modified)
	}
	if o.Webview != "" {
		line("WebView2 %s", o.Webview)
	}
	if o.View.IsProgress() {
		line("View: progress window")
	}

	st, err := d.Status(ctx)
	if err != nil {
		line("downexd: not reachable (%s)", connect.CodeOf(err))
	} else {
		line("downexd %s, running since %s, %d active, %d queued", st.GetVersion(),
			st.GetStartedAt().AsTime().UTC().Format("2006-01-02 15:04 MST"), st.GetActive(), st.GetQueued())
		line("Updates supported: %s", yes(st.GetUpdatesSupported()))
		line("Data folder: %s", path(st.GetDataDir()))
		line("Log folder: %s", path(st.GetLogDir()))
		line("Tools folder: %s", path(st.GetToolsDir()))
	}

	if s, err := d.GetSettings(ctx); err == nil && s.GetSettings() != nil {
		set := s.GetSettings()
		line("Settings: %d at once, %d connections, speed limit %s, category folders %s",
			set.GetMaxConcurrentDownloads(), set.GetDefaultMaxConnections(), limitText(set.GetGlobalSpeedLimit()),
			on(set.GetUseCategoryFolders()))
		line("Browser downloads confirmed: %s; progress window %s; complete dialog %s; clipboard %s",
			on(set.GetConfirmBrowserDownloads()), on(set.GetShowProgressWindow()), on(set.GetShowCompleteDialog()),
			on(set.GetClipboardMonitor()))
		line("Launch at sign-in: %s; tray icon %s; mark downloaded files %s", on(set.GetLaunchAtLogin()),
			on(set.GetShowTrayIcon()), on(set.GetMarkDownloadedFiles()))
		line("Antivirus scan: %s", on(set.GetAvScanEnabled()))
		feed := "not set"
		if set.GetUpdateFeedUrl() != "" {
			feed = "set"
		}
		line("Update address: %s; automatic checks %s", feed, on(set.GetAutoCheckUpdates()))
		line("Proxy: %s", strings.ToLower(strings.TrimPrefix(set.GetProxy().GetMode().String(), "PROXY_MODE_")))
		line("Host rules: %d; Site logins: %d", len(set.GetHostRules()), len(set.GetSiteLogins()))
		line("Video sites: %s; tools updated automatically %s; channel %s", on(set.GetSiteMedia()),
			on(set.GetToolsAutoUpdate()), cmp.Or(set.GetToolsChannel(), "nightly"))
	}

	if t, err := d.GetTools(ctx); err == nil && t.GetStatus() != nil {
		for _, tool := range t.GetStatus().GetTools() {
			state := strings.ToLower(strings.TrimPrefix(tool.GetState().String(), "TOOL_STATE_"))
			text := tool.GetName() + ": " + state
			if tool.GetVersion() != "" {
				text += " " + tool.GetVersion()
			}
			var how []string
			if tool.GetVersion() != "" {
				if tool.GetManaged() {
					how = append(how, "managed")
				} else {
					how = append(how, "found on PATH")
				}
			}
			if tool.GetVerified() != "" {
				how = append(how, tool.GetVerified())
			}
			if tool.GetErrorCode() != "" {
				how = append(how, tool.GetErrorCode())
			}
			if len(how) > 0 {
				text += " (" + strings.Join(how, ", ") + ")"
			}
			line("%s", text)
		}
	}

	if l, err := d.List(ctx); err == nil {
		counts := map[string]int{}
		media := 0
		for _, dl := range l.GetDownloads() {
			counts[StateName(dl.GetState())]++
			if dl.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA {
				media++
			}
		}
		var parts []string
		for state, n := range counts {
			parts = append(parts, fmt.Sprintf("%s: %d", state, n))
		}
		slices.Sort(parts)
		line("Downloads: %d (%d videos or streams); %s", len(l.GetDownloads()), media, strings.Join(parts, ", "))
	}
	return w.String()
}

// limitText is a speed limit for the diagnostics.
func limitText(bps int64) string {
	if bps <= 0 {
		return "none"
	}
	return fmt.Sprintf("%d B/s", bps)
}

// RedactProfile writes a path inside the user's profile with the variable that names its
// folder: on Windows %LOCALAPPDATA%, %APPDATA% or %USERPROFILE% (the longest that matches,
// compared without case), elsewhere "~" for $HOME. Only whole folder names match
// ("C:\Users\Al" is not a prefix of "C:\Users\Alice").
func RedactProfile(p, goos string, env func(string) string) string {
	type prefix struct{ dir, name string }
	var prefixes []prefix
	sep := "/"
	if goos == "windows" {
		sep = `\`
		for _, v := range []string{"LOCALAPPDATA", "APPDATA", "USERPROFILE"} {
			if d := env(v); d != "" {
				prefixes = append(prefixes, prefix{strings.TrimRight(d, `\/`), "%" + v + "%"})
			}
		}
	} else if d := env("HOME"); d != "" {
		prefixes = append(prefixes, prefix{strings.TrimRight(d, "/"), "~"})
	}
	slices.SortFunc(prefixes, func(a, b prefix) int { return len(b.dir) - len(a.dir) })
	for _, pre := range prefixes {
		if pre.dir == "" || len(p) < len(pre.dir) {
			continue
		}
		head, rest := p[:len(pre.dir)], p[len(pre.dir):]
		same := head == pre.dir
		if goos == "windows" {
			same = strings.EqualFold(head, pre.dir)
		}
		if same && (rest == "" || strings.HasPrefix(rest, sep) || goos == "windows" && strings.HasPrefix(rest, "/")) {
			return pre.name + rest
		}
	}
	return p
}

// ---- open-source notices --------------------------------------------------------------

// thirdPartyNotices is a copy of ui/THIRD-PARTY-NOTICES.txt (generated by
// scripts/third-party-notices.mjs: the licences of the Go modules linked into Downex's
// programs and of the frontend's bundled npm packages, and the programs Downex downloads
// separately), which go:embed cannot reach from here; TestNoticesMatchTheWindow keeps the
// two identical.
//
//go:embed THIRD-PARTY-NOTICES.txt
var thirdPartyNotices string

// ThirdPartyNotices is the open-source notices of Downex's programs (Settings → About).
func ThirdPartyNotices() string { return thirdPartyNotices }
